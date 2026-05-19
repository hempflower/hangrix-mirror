package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"io"
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
//   1. password looks like an agent session token (`hgxs_*`) → validate
//      via SessionTokenValidator; the request is authorized for the
//      session's bound repo only. This is the agent push path.
//   2. password looks like a PAT (`hgx_*`) → validate via Validator
//   3. otherwise bcrypt-compare against the user's password_hash
//
// gitCaller wraps the resolved identity. authMethod is "cookie" / "pat" /
// "password" / "session" — used downstream to enforce per-method rules
// (PAT scopes, session repo binding).
type gitCaller struct {
	user       *userdomain.User
	token      *tokendomain.Token            // nil unless authMethod == "pat"
	session    *runnerdomain.AgentSession    // nil unless authMethod == "session"
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
		// Session tokens are the agent's identity for everything the
		// platform exposes — including git push. Per-repo scoping is
		// enforced via canAccessRepo (session.RepoID match) rather
		// than a scope flag.
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
			http.Error(w, "pre-receive: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Replay the original (now decompressed) body to the git receive-pack
	// subprocess. Objects already exist from the unpack above, so
	// receive-pack will skip re-storing them and proceed to ref updates.
	// Clear Content-Encoding since we already decompressed above.
	r.Header.Del("Content-Encoding")
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	h.runStatelessRPC(w, r, "receive-pack", fsPath)

	// PostReceive observers run after the subprocess returns. The client has
	// already received its response by this point so errors are swallowed —
	// we accept temporarily losing a commit_pushed event over corrupting
	// the wire protocol. Use a detached context so a client disconnect
	// doesn't immediately cancel the observer DB writes.
	postCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pusher := pusherFromCaller(caller)
	for _, obs := range h.observers {
		_ = obs.PostReceive(postCtx, repo, fsPath, pusher)
	}
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
