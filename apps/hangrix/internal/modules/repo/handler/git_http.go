package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	tokendomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

// Smart HTTP handlers. Read side (upload-pack) shipped in M2; write side
// (receive-pack) lands in M3 with PAT-aware auth.
//
// Both directions shell out to system git (`upload-pack` / `receive-pack`
// with `--stateless-rpc`): the negotiation protocol is large and battle-
// tested in the C implementation; reimplementing it in Go just to save a
// fork is not worth the surface area.
//
// Auth model:
//
//   - Read (clone/fetch): public repos accept any caller (even anonymous);
//     private repos challenge via WWW-Authenticate.
//   - Write (push): always requires authenticated caller, even for public
//     repos. PAT used over HTTP Basic must carry `repo:write` scope.
//
// Credentials are checked in this order on every Basic-auth attempt:
//  1. password looks like an agent session token (`hgxs_*`) → validate
//     via SessionTokenValidator; the request is authorized for the
//     session's bound repo only. This is the agent push path.
//  2. password looks like a PAT (`hgx_*`) → validate via Validator
//  3. otherwise bcrypt-compare against the user's password_hash
//
// gitCaller wraps the resolved identity. authMethod is "cookie" / "pat" /
// "password" / "session" — used downstream to enforce per-method rules
// (PAT scopes, session repo binding).
type gitCaller struct {
	user       *userdomain.User
	token      *tokendomain.Token         // nil unless authMethod == "pat"
	session    *runnerdomain.AgentSession // nil unless authMethod == "session"
	authMethod string
}

func (g *gitCaller) hasWriteScope() bool {
	switch g.authMethod {
	case "pat":
		if g.token == nil {
			return false
		}
		return g.token.HasScope(tokendomain.ScopeRepoWrite)
	case "session":
		// Contribution-branch model (docs/contribution-branches.md): an
		// agent pushes to its own per-issue namespace ref
		// (refs/heads/issue-<N>/<role>) — that branch IS its contribution.
		// No agent pushes a protected branch; the issue branch and base
		// are advanced server-side. The coarse gate here just requires the
		// session to be bound to an issue with a role key; the actual
		// per-ref namespace check happens in gitReceivePack once the
		// ref-update commands are parsed (see sessionRefAllowed).
		if g.session == nil || g.session.IssueNumber == nil || g.session.RoleKey == "" {
			return false
		}
		return true
	}
	// Cookie and password sessions are equivalent to "full user".
	return true
}

func (h *Handler) gitInfoRefs(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	switch service {
	case "git-upload-pack":
		h.advertiseRefs(w, r, service, false)
	case "git-receive-pack":
		h.advertiseRefs(w, r, service, true)
	default:
		http.Error(w, "service not supported", http.StatusForbidden)
	}
}

// advertiseRefs runs `git <service> --stateless-rpc --advertise-refs` after
// gating on the appropriate auth (write=true for receive-pack). Output is
// wrapped in the standard pkt-line preamble.
func (h *Handler) advertiseRefs(w http.ResponseWriter, r *http.Request, service string, write bool) {
	var fsPath string
	var ok bool
	if write {
		_, fsPath, _, ok = h.authorizeGitWrite(w, r)
	} else {
		_, fsPath, ok = h.authorizeGitRead(w, r)
	}
	if !ok {
		return
	}

	// service is "git-upload-pack" or "git-receive-pack"; trim the "git-"
	// prefix to get the subcommand name.
	cmd := exec.CommandContext(r.Context(), "git", strings.TrimPrefix(service, "git-"), "--stateless-rpc", "--advertise-refs", fsPath)
	out, err := cmd.Output()
	if err != nil {
		http.Error(w, service+" advertise: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(packetLine("# service=" + service + "\n"))
	_, _ = w.Write([]byte("0000"))
	_, _ = w.Write(out)
}

func (h *Handler) gitUploadPack(w http.ResponseWriter, r *http.Request) {
	_, fsPath, ok := h.authorizeGitRead(w, r)
	if !ok {
		return
	}
	h.runStatelessRPC(w, r, "upload-pack", fsPath)
}

func (h *Handler) gitReceivePack(w http.ResponseWriter, r *http.Request) {
	repo, fsPath, caller, ok := h.authorizeGitWrite(w, r)
	if !ok {
		return
	}
	// Refresh the protection rules sidecar so the pre-receive hook sees the
	// current ruleset. Idempotently re-installs the hook script too, which
	// lets repos predating this feature pick it up on first push.
	rules, err := h.protections.List(r.Context(), repo.ID)
	if err != nil {
		http.Error(w, "load protections: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.storage.SyncProtectionRules(fsPath, rules); err != nil {
		http.Error(w, "sync protections: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Buffer the full request body so we can parse pkt-line ref commands
	// and, if needed, extract pack objects before the git subprocess runs.
	// Use decodeRequestBody so transparent gzip decompression (git CLI
	// sends large pushes with Content-Encoding: gzip) is applied before
	// we inspect the pkt-line stream.
	decodedBody, err := decodeRequestBody(r)
	if err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer decodedBody.Close()
	bodyBytes, err := io.ReadAll(decodedBody)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Parse ref-update commands from the pkt-line stream. packStart is the
	// byte offset where the binary pack data begins (past the "0000" flush
	// and "PACK" signature). If the body doesn't contain ref commands (e.g.
	// a zero-ref push or a corrupted stream), refUpdates is empty and
	// packStart is 0.
	refUpdates, packStart := parseReceivePackRefs(bodyBytes)

	// Per-ref ACL for agent sessions (contribution-branch model): a session
	// may only update refs inside its own per-issue namespace
	// (refs/heads/issue-<N>/<role>[/...]) or create new tags. Any attempt to
	// touch the issue branch, base, another role's namespace, or update an
	// existing tag is rejected before git runs. Human callers
	// (cookie/pat/password) are governed by branch protections + canAccessRepo
	// instead.
	if caller != nil && caller.authMethod == "session" && caller.session != nil {
		base := sessionNamespacePrefix(caller.session)
		for _, u := range refUpdates {
			if !sessionRefUpdateAllowed(u.RefName, u.OldSHA, base) {
				if strings.HasPrefix(u.RefName, "refs/tags/") {
					http.Error(w, "per-ref ACL: agent sessions may only create new tags", http.StatusForbidden)
					return
				}
				http.Error(w, "per-ref ACL: agent session may only push to "+base+"/<slug>", http.StatusForbidden)
				return
			}
		}
	}

	// If the push touches any ref, extract the pack objects into the bare
	// repo so that PreReceive observers (issue fast-forward check) can
	// resolve the new SHAs. We feed the pack data to `git unpack-objects`
	// which silently skips objects already in the store.
	if len(refUpdates) > 0 && packStart > 0 && packStart < len(bodyBytes) {
		unpack := exec.CommandContext(r.Context(), "git", "unpack-objects")
		unpack.Dir = fsPath
		unpack.Stdin = bytes.NewReader(bodyBytes[packStart:])
		// unpack-objects writes progress to stderr; discard it.
		_ = unpack.Run()
		// Ignore errors: unpack-objects will complain about duplicate
		// objects already in the repo, but that's harmless — the goal
		// was to ensure new commits are resolvable, and they are now.
	}

	// PreReceive observers get the parsed ref updates. The pack objects are
	// already in the repo, so observers can resolve new SHAs via go-git.
	for _, obs := range h.observers {
		if err := obs.PreReceive(r.Context(), repo, fsPath, refUpdates); err != nil {
			if errors.Is(err, domain.ErrBranchDiverged) {
				http.Error(w, "pre-receive: "+err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, "pre-receive: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Replay the original (now decompressed) body to the git receive-pack
	// subprocess. Objects already exist from the unpack above, so
	// receive-pack will skip re-storing them and proceed to ref updates.
	// Clear Content-Encoding since we already decompressed above.
	//
	// We buffer stdout (rather than wiring it directly to w) so that we can
	// inject sideband progress messages after PostReceive runs. A typical
	// receive-pack response is a few hundred bytes of pkt-line status; the
	// pack data is on stdin, not stdout. Sideband gating is done inside
	// runReceivePackWithSideband by inspecting the actual receive-pack
	// stdout (hasSidebandResponse), which is more reliable than checking
	// push capabilities.
	r.Header.Del("Content-Encoding")
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	h.runReceivePackWithSideband(w, r, fsPath, repo, caller, refUpdates)

	// Push may have changed refs (branch / tag updates, force-pushes,
	// etc.) — drop every git-read cache key for this repo so the next
	// page load sees the new state.
	postCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h.invalidateCache(postCtx, repo.ID)
}

// pusherFromCaller maps the resolved write caller to a domain.Pusher.
// Agent sessions surface their snapshot RoleKey (the same value used when
// the agent posts comments / review_vote, so the timeline shows one
// consistent "@agent-<role>" identity); everything else surfaces the
// user id. A session row with an empty RoleKey would render as an
// anonymous push — better to fall back to the session creator than to
// show a dash.
func pusherFromCaller(c *gitCaller) domain.Pusher {
	if c == nil {
		return domain.Pusher{}
	}
	if c.authMethod == "session" && c.session != nil {
		if c.session.RoleKey != "" {
			return domain.Pusher{AgentRole: c.session.RoleKey}
		}
		return domain.Pusher{UserID: c.session.CreatedBy}
	}
	if c.user != nil {
		return domain.Pusher{UserID: c.user.ID}
	}
	return domain.Pusher{}
}

// runStatelessRPC streams the request body into `git <sub> --stateless-rpc`
// stdin and the subprocess stdout into the response body. Same shape for
// upload-pack and receive-pack — only the subcommand and content-type differ.
func (h *Handler) runStatelessRPC(w http.ResponseWriter, r *http.Request, sub, fsPath string) {
	body, err := decodeRequestBody(r)
	if err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer body.Close()

	cmd := exec.CommandContext(r.Context(), "git", sub, "--stateless-rpc", fsPath)
	cmd.Stdin = body
	cmd.Stdout = w
	cmd.Stderr = io.Discard

	w.Header().Set("Content-Type", "application/x-git-"+sub+"-result")
	w.Header().Set("Cache-Control", "no-cache")
	// Once Stdout is wired to w, the first Write triggers WriteHeader(200);
	// any subprocess error after that surfaces as a protocol-level error to
	// the git client, which is fine.
	_ = cmd.Run()
}

// runReceivePackWithSideband runs the git receive-pack subprocess with stdout
// buffered, runs PostReceive observers, and — when contributions were
// recognised — injects extra sideband-2 progress messages into the response
// before the terminating flush pkt. The caller's response writer receives the
// complete, possibly augmented pkt-line stream.
func (h *Handler) runReceivePackWithSideband(w http.ResponseWriter, r *http.Request, fsPath string, repo *domain.Repo, caller *gitCaller, refUpdates []domain.PushRefUpdate) {
	body, err := decodeRequestBody(r)
	if err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer body.Close()

	// Collect receive-pack stdout into a buffer so we can inject sideband
	// messages before the terminating "0000" flush pkt.
	var stdout bytes.Buffer
	cmd := exec.CommandContext(r.Context(), "git", "receive-pack", "--stateless-rpc", fsPath)
	cmd.Stdin = body
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard

	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	if err := cmd.Run(); err != nil {
		// git receive-pack may have updated refs before the process exited
		// abnormally (signal, context cancellation, etc.). PostReceive
		// observers still run so contributions are recognised, but the
		// stdout may be truncated — injectContributionHints handles that
		// via lastOuterFlush rather than a naive bytes.LastIndex.
		log.Printf("repo: receive-pack error for repo %d: %v (stdout=%d bytes)", repo.ID, err, stdout.Len())
	}

	// PostReceive observers run after the subprocess returns. Use a detached
	// context so a client disconnect doesn't immediately cancel the observer
	// DB writes.
	postCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pusher := pusherFromCaller(caller)
	var allContribs []domain.PostReceiveContrib
	for _, obs := range h.observers {
		contribs, _ := obs.PostReceive(postCtx, repo, fsPath, pusher, refUpdates)
		allContribs = append(allContribs, contribs...)
	}

	// Inject contribution hints as sideband-2 progress messages before the
	// terminating flush pkt so the pusher sees `remote:` lines with
	// contribution IDs and next-step hints. Gated on a response-side check:
	// if the stdout stream's first pkt-line payload starts with a sideband
	// channel marker (0x01/0x02/0x03), the response is multiplexed and we
	// can safely inject. Checking the actual output is more reliable than
	// parsing push capabilities because receive-pack's capability set does
	// not include side-band/side-band-64k (fetch-side / upload-pack only).
	if hasSidebandResponse(stdout.Bytes()) && len(allContribs) > 0 {
		h.injectContributionHints(&stdout, allContribs)
	}

	_, _ = w.Write(stdout.Bytes())
}

// injectContributionHints inserts sideband-2 progress pkt-lines before the
// terminating flush pkt in buf. The injected messages carry contribution_id
// and next-step hints so the agent that pushed the branch doesn't need a
// follow-up contribution_list call.
//
// Under --stateless-rpc, receive-pack produces a double-framed pkt-line stream:
// an outer sideband layer whose pack-data channel (0x01) embeds another layer
// of pkt-line framing that also ends with "0000". Both flush sequences are
// adjacent, giving "…0000" (inner) + "0000" (outer). We parse the pkt-line
// structure to find the last standalone "0000" flush (not the inner one inside
// a sideband pkt-line payload) and inject the contribution hints immediately
// before it. When no standalone flush is present (e.g. receive-pack killed
// mid-write, truncated output) we append a fresh one so the git client always
// sees a properly terminated stream.
func (h *Handler) injectContributionHints(buf *bytes.Buffer, contribs []domain.PostReceiveContrib) {
	raw := buf.Bytes()

	// Walk the pkt-line stream to find the last standalone "0000" flush —
	// one that is NOT inside another pkt-line's payload. The inner flush
	// is embedded inside a sideband-1 pkt-line; its "0000" bytes are part
	// of that outer pkt-line's data and must not be split.
	idx := lastOuterFlush(raw)
	var head []byte
	var tail string
	if idx >= 0 {
		head = raw[:idx]
		tail = string(raw[idx:]) // snapshot before buf.Reset/Write overwrite
	} else {
		head = raw
		tail = "0000"
	}

	buf.Reset()
	_, _ = buf.Write(head)

	for _, c := range contribs {
		shortSHA := c.HeadSHA
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		msg := fmt.Sprintf("contribution_id: %d | ref: %s | role: %s | head: %s",
			c.ContributionID, strings.TrimPrefix(c.RefName, "refs/heads/"), c.AgentRole, shortSHA)
		hint := "Next: use contribution_set_meta to set title/description; " +
			"contribution_read for metadata, review status & checkout_hint (fetch branch locally to inspect diff). " +
			"No need for contribution_list to discover this ID."
		for _, line := range []string{msg, hint} {
			buf.Write(sidebandPktLine(line))
		}
	}

	_, _ = buf.WriteString(tail)
}

// lastOuterFlush walks the pkt-line stream and returns the byte position of the
// last standalone "0000" flush pkt — one that is NOT inside another pkt-line's
// payload. Returns -1 when no standalone flush exists (truncated stream).
//
// This is needed because receive-pack output double-frames: an outer sideband
// layer (channel 0x01) wraps inner pkt-line status, and that inner framing
// includes its own "0000" flush. A naive bytes.LastIndex for "0000" would find
// the inner flush when the outer flush is missing (e.g. receive-pack killed
// mid-write), and splitting at that position would break the outer pkt-line
// framing — the length prefix would claim bytes that are no longer present.
func lastOuterFlush(raw []byte) int {
	pos := 0
	lastFlush := -1
	for pos < len(raw) {
		if pos+4 > len(raw) {
			break
		}
		// Standalone flush pkt: exactly "0000" at the current position.
		if string(raw[pos:pos+4]) == "0000" {
			lastFlush = pos
			pos += 4
			continue
		}
		// Parse the pkt-line length (4 hex digits).
		pktLen, err := hex.DecodeString(string(raw[pos : pos+4]))
		if err != nil || len(pktLen) != 2 {
			break
		}
		pl := int(pktLen[0])<<8 | int(pktLen[1])
		if pl < 4 || pos+pl > len(raw) {
			break
		}
		pos += pl
	}
	return lastFlush
}

// sidebandPktLine encodes text as a sideband-2 (progress) pkt-line suitable for
// injection into a git receive-pack response stream. The git client renders
// sideband-2 as a `remote:` line. The text is truncated to fit within the git
// pkt-line maximum of 65520 bytes (0xfff0).
func sidebandPktLine(text string) []byte {
	// Data is: sideband byte (0x02) + text + "\n"
	data := "\x02" + text + "\n"
	// Git pkt-line max is 65520 bytes (0xfff0); the 4-byte hex length prefix
	// must fit in exactly 4 hex digits, so totalLen must not exceed 0xffff.
	// Cap at 0xfff0 to stay within the protocol limit.
	maxDataLen := 0xfff0 - 4 // 65516
	if len(data) > maxDataLen {
		data = data[:maxDataLen-1] + "\n" // keep trailing newline
	}
	totalLen := 4 + len(data)
	return fmt.Appendf(nil, "%04x%s", totalLen, data)
}

// authorizeGitRead — public repos: open to anyone. Private repos: require
// authenticated owner or admin.
func (h *Handler) authorizeGitRead(w http.ResponseWriter, r *http.Request) (*domain.Repo, string, bool) {
	owner := chi.URLParam(r, "owner")
	namegit := chi.URLParam(r, "namegit")
	repoName := strings.TrimSuffix(namegit, ".git")
	if !usernameRe.MatchString(owner) || !repoNameRe.MatchString(repoName) {
		http.NotFound(w, r)
		return nil, "", false
	}

	repo, fsPath, ok := h.loadGitRepo(w, r, owner, repoName)
	if !ok {
		return nil, "", false
	}

	if repo.Visibility != domain.VisibilityPublic {
		caller, ok := h.identifyGitCaller(r)
		if !ok {
			challengeBasicAuth(w)
			return nil, "", false
		}
		if !h.canAccessRepo(r.Context(), caller, repo, false) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return nil, "", false
		}
	}
	return repo, fsPath, true
}

// authorizeGitWrite — always requires authentication. Owner or admin only.
// PATs must carry repo:write scope. Returns the resolved caller so the
// receive-pack handler can attribute commit_pushed events to the right
// identity (human user vs. agent session).
func (h *Handler) authorizeGitWrite(w http.ResponseWriter, r *http.Request) (*domain.Repo, string, *gitCaller, bool) {
	owner := chi.URLParam(r, "owner")
	namegit := chi.URLParam(r, "namegit")
	repoName := strings.TrimSuffix(namegit, ".git")
	if !usernameRe.MatchString(owner) || !repoNameRe.MatchString(repoName) {
		http.NotFound(w, r)
		return nil, "", nil, false
	}

	repo, fsPath, ok := h.loadGitRepo(w, r, owner, repoName)
	if !ok {
		return nil, "", nil, false
	}

	caller, ok := h.identifyGitCaller(r)
	if !ok {
		challengeBasicAuth(w)
		return nil, "", nil, false
	}
	if !h.canAccessRepo(r.Context(), caller, repo, true) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, "", nil, false
	}
	if !caller.hasWriteScope() {
		http.Error(w, "token lacks repo:write scope", http.StatusForbidden)
		return nil, "", nil, false
	}
	return repo, fsPath, caller, true
}

func (h *Handler) loadGitRepo(w http.ResponseWriter, r *http.Request, owner, repoName string) (*domain.Repo, string, bool) {
	ctx := r.Context()
	resolved, err := h.resolver.ResolveOwner(ctx, owner)
	if err != nil {
		// Don't differentiate "no such owner" from "no such repo" — same
		// 404 either way.
		http.NotFound(w, r)
		return nil, "", false
	}
	repo, err := h.store.GetByOwnerAndName(ctx, domain.OwnerKind(resolved.Kind), resolved.ID, repoName)
	if err != nil {
		http.NotFound(w, r)
		return nil, "", false
	}
	fsPath, err := h.storage.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		http.NotFound(w, r)
		return nil, "", false
	}
	return repo, fsPath, true
}

// canAccessRepo answers "may this caller read or write the repo?" — the
// smart-HTTP wrappers further restrict writes via hasWriteScope. The
// caller can be one of:
//
//   - A user (cookie / PAT / password). User-owned repos allow the
//     owner; org-owned repos allow any member to read and only
//     owner-role members to write. Admin always passes.
//   - An agent session. Authorized only for the repo the session is
//     bound to. The session is itself bound to a (repo, issue) when
//     the spawner creates it; we treat that as the authority.
func (h *Handler) canAccessRepo(ctx context.Context, caller *gitCaller, repo *domain.Repo, write bool) bool {
	if caller == nil {
		return false
	}
	if caller.authMethod == "session" && caller.session != nil {
		// Session is bound to a specific (repo, issue). Refuse any
		// access to a different repo even if the same operator
		// happens to have another active session — keeps the blast
		// radius of a leaked agent token strictly per-session.
		if caller.session.RepoID == nil || *caller.session.RepoID != repo.ID {
			return false
		}
		// Terminal / archived sessions are rejected upstream by the
		// validator; double-check here so a token whose row was just
		// flipped doesn't sneak through a race.
		if caller.session.Status.Terminal() {
			return false
		}
		return true
	}
	if caller.user == nil {
		return false
	}
	user := caller.user
	if user.Role == userdomain.RoleAdmin {
		return true
	}
	switch repo.OwnerKind {
	case domain.OwnerKindUser:
		if user.ID == repo.OwnerID {
			return true
		}
		// Check repo_members for user-owned repos.
		m, err := h.members.GetMember(ctx, repo.ID, user.ID)
		if err != nil {
			return false
		}
		if write {
			return m.Role == domain.MemberRoleWrite
		}
		return m.Role == domain.MemberRoleRead || m.Role == domain.MemberRoleWrite
	case domain.OwnerKindOrg:
		role, ok, err := h.resolver.Membership(ctx, repo.OwnerID, user.ID)
		if err != nil || !ok {
			return false
		}
		if write {
			return role == orgdomain.RoleOwner
		}
		return true
	}
	return false
}

func challengeBasicAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="hangrix"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

// identifyGitCaller resolves a request to a *gitCaller. Tries (in order):
//
//  1. Session cookie injected by RequireAuth — kept so a logged-in browser
//     can hit these endpoints uniformly. Cookie auth has no scope.
//  2. HTTP Basic with an agent session token (`hgxs_*`) — validates via
//     the runner module's SessionTokenValidator. The resulting caller is
//     bound to the session's RepoID; canAccessRepo enforces the match.
//  3. HTTP Basic with a PAT-shaped password (`hgx_*`) — validates via the
//     token module. Captures the resolved token so write paths can check
//     its scope.
//  4. HTTP Basic with a raw password — bcrypt-compares the user's stored
//     password_hash. Same trust level as cookie.
func (h *Handler) identifyGitCaller(r *http.Request) (*gitCaller, bool) {
	if u, ok := authdomain.UserFromRequest(r); ok && !u.Disabled {
		return &gitCaller{user: u, authMethod: "cookie"}, true
	}

	username, password, ok := r.BasicAuth()
	if !ok {
		return nil, false
	}

	ctx := r.Context()

	// hgxs_ short-circuits before hgx_ because both prefixes start with
	// "hgx" — checking the longer prefix first prevents an agent token
	// from falling through to the PAT validator (which would reject it).
	if h.sessions != nil && strings.HasPrefix(password, "hgxs_") {
		sess, err := h.sessions.ValidateSessionToken(ctx, password)
		if err == nil && sess != nil {
			// We don't materialise a user here — agents have no row in
			// `users`. Downstream `canAccessRepo` checks the session's
			// RepoID directly when authMethod == "session". The
			// `username` field of HTTP Basic is ignored (git CLI uses
			// "x" by convention).
			return &gitCaller{session: sess, authMethod: "session"}, true
		}
		// Soft-fail through to PAT/password — same rationale as the
		// PAT path below.
	}

	// PAT-shaped credentials short-circuit the password path. We use a
	// prefix check instead of trying the validator unconditionally because
	// the validator does a DB lookup, and we'd rather fall straight to
	// bcrypt for raw passwords without an extra round-trip.
	if strings.HasPrefix(password, "hgx_") {
		tok, user, err := h.tokens.ValidateToken(ctx, password)
		if err == nil && user != nil && !user.Disabled {
			// PAT carries its own identity; we ignore the username field
			// of Basic auth (it's typically the same user, but the token
			// is what's authoritative).
			return &gitCaller{user: user, token: tok, authMethod: "pat"}, true
		}
		// Soft-fail PAT errors (not found / expired / revoked / invalid)
		// → fall through to password path. The cost is one extra failed
		// lookup in the rare case a user's actual password happens to
		// start with `hgx_`.
	}

	u, err := h.users.GetByUsername(ctx, username)
	if err != nil || u.Disabled {
		return nil, false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, false
	}
	return &gitCaller{user: u, authMethod: "password"}, true
}

// sessionNamespacePrefix returns the full ref prefix an agent session is
// allowed to write: refs/heads/issue-<IssueNumber>/<RoleKey>. Returns "" when
// the session lacks an issue/role binding (callers treat that as "deny all").
func sessionNamespacePrefix(sess *runnerdomain.AgentSession) string {
	if sess == nil || sess.IssueNumber == nil || sess.RoleKey == "" {
		return ""
	}
	return fmt.Sprintf("refs/heads/issue-%d/%s", *sess.IssueNumber, sess.RoleKey)
}

// refWithinNamespace reports whether refName is the namespace base itself or a
// child of it (base + "/..."). An empty base denies everything.
func refWithinNamespace(refName, base string) bool {
	if base == "" {
		return false
	}
	return refName == base || strings.HasPrefix(refName, base+"/")
}

// sessionRefUpdateAllowed applies the agent-session push policy. Contribution
// branches must live under the session's own refs/heads/issue-<N>/<role>
// namespace, while tag pushes are allowed only when creating a new tag. Any
// existing ref update or delete is rejected to preserve write-once semantics.
func sessionRefUpdateAllowed(refName, oldSHA, base string) bool {
	if base == "" {
		return false
	}
	if strings.HasPrefix(refName, "refs/tags/") {
		return isZeroSHA(oldSHA)
	}
	if !refWithinNamespace(refName, base) {
		return false
	}
	if refName == base {
		return false
	}
	return isZeroSHA(oldSHA)
}

// isZeroSHA reports whether sha is the git all-zero object id — the old-SHA of
// a ref create, or the new-SHA of a ref delete. An empty string counts as
// zero. Used to distinguish "creating a new contribution branch" (old=zero,
// allowed) from "updating/deleting an existing one" (old≠zero, rejected by the
// immutability gate).
func isZeroSHA(sha string) bool {
	if sha == "" {
		return true
	}
	for _, r := range sha {
		if r != '0' {
			return false
		}
	}
	return true
}

// parseReceivePackRefs extracts ref-update commands from a git receive-pack
// pkt-line stream. Returns the parsed updates and the byte offset where the
// binary pack data begins (after the "0000" flush pkt). packStart is 0 when
// no ref commands were found or the stream is malformed.
//
// Format (gitprotocol-pack(5)):
//
//	<4-hex-len><old-sha> <new-sha> <refname>\0<capabilities>
//	...
//	0000
//	PACK<binary>
func parseReceivePackRefs(data []byte) ([]domain.PushRefUpdate, int) {
	if len(data) < 4 {
		return nil, 0
	}

	var refs []domain.PushRefUpdate
	pos := 0
	for pos+4 <= len(data) {
		// Read 4-byte hex length prefix.
		lenHex := string(data[pos : pos+4])
		if lenHex == "0000" {
			// Flush packet marks end of ref advertisement. The pack
			// data starts after this 4-byte token.
			return refs, pos + 4
		}

		pktLen, err := hex.DecodeString(lenHex)
		if err != nil || len(pktLen) != 2 {
			break
		}
		pl := int(pktLen[0])<<8 | int(pktLen[1])
		if pl < 4 {
			// 0001, 0002, 0003 are reserved tokens — skip and continue.
			pos += 4
			continue
		}

		payloadEnd := pos + pl
		if payloadEnd > len(data) {
			break
		}
		payload := data[pos+4 : payloadEnd]
		pos = payloadEnd

		// Skip side-band / keep-alive / shallow / deepen / etc.
		// Ref commands always contain a space between old and new SHA.
		if !bytes.Contains(payload, []byte(" ")) {
			continue
		}

		// Payload: "<old-sha> <new-sha> <refname>\0<capabilities>"
		line := string(payload)
		// Strip trailing NUL and capabilities.
		if idx := strings.IndexByte(line, 0); idx >= 0 {
			line = line[:idx]
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			continue
		}
		refs = append(refs, domain.PushRefUpdate{
			OldSHA:  parts[0],
			NewSHA:  parts[1],
			RefName: parts[2],
		})
	}
	return refs, pos
}

// hasSidebandResponse checks whether the stdout from `git receive-pack
// --stateless-rpc` is sideband-multiplexed by inspecting the first pkt-line's
// payload. Under --stateless-rpc, receive-pack unconditionally uses sideband
// for its response: the first byte after each pkt-line's 4-hex-digit length
// prefix is a channel marker (0x01=pack, 0x02=progress, 0x03=error). A plain
// (non-multiplexed) pkt-line would start with printable text instead, so
// looking for channel bytes in the initial output is more reliable than
// checking the push request's capabilities — the request-side receive-pack
// capability set does not include side-band/side-band-64k (those are
// upload-pack / fetch-side).
func hasSidebandResponse(data []byte) bool {
	if len(data) < 5 {
		return false
	}
	// Read the first pkt-line's 4-byte hex length prefix.
	lenHex := string(data[:4])
	if lenHex == "0000" {
		// Empty stream — nothing to multiplex, but also nothing to inject into.
		return false
	}
	pktLen, err := hex.DecodeString(lenHex)
	if err != nil || len(pktLen) != 2 {
		return false
	}
	pl := int(pktLen[0])<<8 | int(pktLen[1])
	if pl < 5 || pl > len(data) {
		// Need at least 5 bytes: 4-byte prefix + 1 data byte (channel marker).
		return false
	}
	// The first data byte after the length prefix is the payload's first byte.
	// In sideband mode this is a channel marker (0x01, 0x02, or 0x03).
	ch := data[4]
	return ch == 1 || ch == 2 || ch == 3
}

// packetLine encodes one Git wire-protocol packet line: 4-hex-digit length
// prefix (covering the prefix itself) followed by the payload.
func packetLine(payload string) []byte {
	return fmt.Appendf(nil, "%04x%s", len(payload)+4, payload)
}

// decodeRequestBody returns a reader over the request body, transparently
// gunzipping when the client used Content-Encoding: gzip (git CLI does this
// for larger POSTs).
func decodeRequestBody(r *http.Request) (io.ReadCloser, error) {
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		return gzipCloser{Reader: gz, body: r.Body, gz: gz}, nil
	}
	return r.Body, nil
}

type gzipCloser struct {
	io.Reader
	body io.Closer
	gz   io.Closer
}

func (g gzipCloser) Close() error {
	_ = g.gz.Close()
	return g.body.Close()
}
