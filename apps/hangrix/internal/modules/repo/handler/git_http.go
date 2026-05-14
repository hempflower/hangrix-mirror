package handler

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
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
//   1. password looks like a PAT (`hgx_*`) → validate via Validator
//   2. otherwise bcrypt-compare against the user's password_hash
//
// gitCaller wraps the resolved identity. authMethod is "cookie" / "pat" /
// "password" — used downstream to enforce PAT scopes.
type gitCaller struct {
	user       *userdomain.User
	token      *tokendomain.Token // nil unless authMethod == "pat"
	authMethod string
}

func (g *gitCaller) hasWriteScope() bool {
	// Cookie and password sessions are equivalent to "full user"; only PATs
	// are scope-limited. (Future: we may revisit cookie scopes when a web
	// flow needs to mint a narrow session.)
	if g.authMethod != "pat" || g.token == nil {
		return true
	}
	return g.token.HasScope(tokendomain.ScopeRepoWrite)
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
		_, fsPath, ok = h.authorizeGitWrite(w, r)
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
	repo, fsPath, ok := h.authorizeGitWrite(w, r)
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
	h.runStatelessRPC(w, r, "receive-pack", fsPath)
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
		if !canAccessRepo(caller.user, repo) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return nil, "", false
		}
	}
	return repo, fsPath, true
}

// authorizeGitWrite — always requires authentication. Owner or admin only.
// PATs must carry repo:write scope.
func (h *Handler) authorizeGitWrite(w http.ResponseWriter, r *http.Request) (*domain.Repo, string, bool) {
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

	caller, ok := h.identifyGitCaller(r)
	if !ok {
		challengeBasicAuth(w)
		return nil, "", false
	}
	if !canAccessRepo(caller.user, repo) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, "", false
	}
	if !caller.hasWriteScope() {
		http.Error(w, "token lacks repo:write scope", http.StatusForbidden)
		return nil, "", false
	}
	return repo, fsPath, true
}

func (h *Handler) loadGitRepo(w http.ResponseWriter, r *http.Request, owner, repoName string) (*domain.Repo, string, bool) {
	ctx := r.Context()
	ownerUser, err := h.users.GetByUsername(ctx, owner)
	if err != nil {
		// Don't differentiate "no such user" from "no such repo" — same
		// 404 either way.
		http.NotFound(w, r)
		return nil, "", false
	}
	repo, err := h.store.GetByOwnerAndName(ctx, ownerUser.ID, repoName)
	if err != nil {
		http.NotFound(w, r)
		return nil, "", false
	}
	fsPath, err := h.storage.ResolvePath(repo.OwnerUsername, repo.Name)
	if err != nil {
		http.NotFound(w, r)
		return nil, "", false
	}
	return repo, fsPath, true
}

func canAccessRepo(user *userdomain.User, repo *domain.Repo) bool {
	return user.ID == repo.OwnerID || user.Role == userdomain.RoleAdmin
}

func challengeBasicAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="hangrix"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

// identifyGitCaller resolves a request to a *gitCaller. Tries (in order):
//
//  1. Session cookie injected by RequireAuth — kept so a logged-in browser
//     can hit these endpoints uniformly. Cookie auth has no scope.
//  2. HTTP Basic with a PAT-shaped password (`hgx_*`) — validates via the
//     token module. Captures the resolved token so write paths can check
//     its scope.
//  3. HTTP Basic with a raw password — bcrypt-compares the user's stored
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
