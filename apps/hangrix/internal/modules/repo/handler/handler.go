// Package handler exposes the repo module's HTTP surface: CRUD over
// repository metadata plus read-only git endpoints (refs, commits, tree,
// blob, diff). Authorization is enforced here (not in SQL): public repos are
// visible to any authenticated user; private repos are visible only to the
// owner or an admin.
package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"github.com/hangrix/hangrix/apps/hangrix/internal/kv"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	tokendomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

// maxBlobBytes caps blob responses. The HTTP layer refuses to return a file
// larger than this (clients should hit a future raw-streaming endpoint for
// big files) — 1 MiB is plenty for source code; larger payloads bloat JSON.
const maxBlobBytes = 1 << 20 // 1 MiB

// Cache TTLs per the product spec: short enough to keep the view fresh after
// writes, long enough to absorb repeated reads from the same page load.
const (
	refsCacheTTL    = 15 * time.Second
	treeViewCacheTTL = 30 * time.Second
	commitsCacheTTL  = 20 * time.Second
)

// repoNameRe is the canonical repo-name regex. Must start with an
// alphanumeric or underscore, then up to 99 more chars from a slightly wider
// class. Mirrors the filesystem-safety set so a valid name is always a valid
// path component.
var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)

// usernameRe is reused for the {owner} path segment so we can fail fast on
// obviously bad input before hitting the database.
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)

type Handler struct {
	store       domain.Store
	protections domain.ProtectionStore
	members     domain.MemberStore
	variables   domain.VariableStore
	storage     *infra.Storage
	git         gitdomain.Git
	users       userdomain.Repo
	orgs        orgdomain.OrgRepo
	resolver    orgdomain.Resolver
	tokens      tokendomain.Validator
	// sessions validates hgxs_ agent session tokens for git push. Nil
	// in test configurations that don't load the runner module; the
	// inline auth path nil-checks before consulting it.
	sessions   runnerdomain.SessionTokenValidator
	middleware authdomain.Middleware
	cache      *kv.RepoCache
	guards     []domain.BranchWriteGuard
	observers  []domain.PushObserver
}

type HandlerDeps struct {
	Store       domain.Store
	Protections domain.ProtectionStore
	Members     domain.MemberStore
	Variables   domain.VariableStore
	Storage     *infra.Storage
	Git         gitdomain.Git
	Users       userdomain.Repo
	// Orgs + Resolver come from the org module. Orgs handles
	// "is caller a member of this org" / "what's their role" queries; the
	// Resolver turns the path-segment {owner} into an Owner (kind, id, name).
	Orgs     orgdomain.OrgRepo
	Resolver orgdomain.Resolver
	Tokens   tokendomain.Validator
	// Sessions resolves an `hgxs_*` Basic-auth password to its
	// agent_session row so the agent path can `git push` over the same
	// Smart-HTTP endpoint humans use. Optional — repo handler works
	// without it (no agent push support).
	Sessions   runnerdomain.SessionTokenValidator
	Middleware authdomain.Middleware
	// Guards is injected as the slice of every BranchWriteGuard registered
	// in the ioc container — currently 0 or 1 element (the issue module's
	// guard). The handler iterates in order; first non-nil error wins.
	Guards []domain.BranchWriteGuard
	// Observers receive pre/post-receive callbacks for the smart-HTTP push
	// pipeline. M4's issue module uses this to sync its sidecar before each
	// push and append commit_pushed events afterwards.
	Observers []domain.PushObserver
	// Cache provides the Redis-backed read cache for hot git endpoints
	// (refs, tree-view, commits). Nil when not configured (e.g. tests).
	Cache *kv.RepoCache
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		store:       deps.Store,
		protections: deps.Protections,
		members:     deps.Members,
		variables:   deps.Variables,
		storage:     deps.Storage,
		git:         deps.Git,
		users:       deps.Users,
		orgs:        deps.Orgs,
		resolver:    deps.Resolver,
		tokens:      deps.Tokens,
		sessions:    deps.Sessions,
		middleware:  deps.Middleware,
		cache:       deps.Cache,
		guards:      deps.Guards,
		observers:   deps.Observers,
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/repos", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Post("/", h.create)
		r.Get("/me", h.listMine)
		// Static segment, must register before the `/{owner}/{name}`
		// catch-all so chi picks the literal route on a GET for
		// `/api/repos/default-agents-yaml`.
		r.Get("/default-agents-yaml", h.getDefaultAgentsYAML)
		r.Get("/{owner}/{name}", h.getOne)
		r.Patch("/{owner}/{name}", h.patchOne)
		r.Delete("/{owner}/{name}", h.deleteOne)

		r.Get("/{owner}/{name}/refs", h.getRefs)
		r.Get("/{owner}/{name}/commits", h.listCommits)
		r.Get("/{owner}/{name}/commits/{sha}", h.getCommit)
		r.Get("/{owner}/{name}/commits/{sha}/contains", h.getContainingRefs)
		r.Get("/{owner}/{name}/tree", h.getTree)
		r.Get("/{owner}/{name}/tree-view", h.getTreeView)
		r.Get("/{owner}/{name}/blob", h.getBlob)
		r.Get("/{owner}/{name}/diff", h.getDiff)
		// Archive uses a trailing wildcard so refs containing "/" (e.g.
		// "feature/foo.zip") survive the URL. Extension is parsed off the
		// captured value.
		r.Get("/{owner}/{name}/archive/*", h.getArchive)

		// Branch / tag write operations. The DELETE routes use chi's
		// trailing-wildcard pattern so names containing "/" (e.g.
		// "feature/foo") round-trip correctly through the URL path.
		r.Post("/{owner}/{name}/branches", h.createBranch)
		r.Delete("/{owner}/{name}/branches/*", h.deleteBranch)
		r.Post("/{owner}/{name}/tags", h.createTag)
		r.Delete("/{owner}/{name}/tags/*", h.deleteTag)

		// Branch protection rules. Listed by any repo reader; mutated only
		// by owner / admin (resolveRepoForManage is called in each handler).
		r.Get("/{owner}/{name}/branch-protections", h.listProtections)
		r.Post("/{owner}/{name}/branch-protections", h.createProtection)
		r.Patch("/{owner}/{name}/branch-protections/{id}", h.updateProtection)
		r.Delete("/{owner}/{name}/branch-protections/{id}", h.deleteProtection)

		// Repo member management. Only for user-owned repos; org repos
		// return 400. Manage-only: only owner/admin can add/update/remove
		// members; any member (or owner) can list.
		r.Get("/{owner}/{name}/members", h.listMembers)
		r.Post("/{owner}/{name}/members", h.addMember)
		r.Patch("/{owner}/{name}/members/{username}", h.patchMember)
		r.Delete("/{owner}/{name}/members/{username}", h.removeMember)

		// Repo variables and secrets. Manage-only: only owner/admin
		// can create/update/delete/list.
		r.Get("/{owner}/{name}/variables", h.listVariables)
		r.Post("/{owner}/{name}/variables", h.createVariable)
		r.Patch("/{owner}/{name}/variables/{varName}", h.updateVariable)
		r.Delete("/{owner}/{name}/variables/{varName}", h.deleteVariable)
	})

	r.Route("/api/users/{username}/repos", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/", h.listByUsername)
	})

	r.Route("/api/orgs/{org}/repos", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/", h.listByOrg)
	})

	// Ownership transfer. Sits under /api/repos rather than the org module
	// since the caller is identified by the source repo, not by the target
	// owner. POST not PATCH because the action is non-idempotent and
	// rearranges filesystem state.
	r.With(h.middleware.RequireAuth).
		Post("/api/repos/{owner}/{name}/transfer", h.transfer)

	// Smart HTTP. Auth is handled inline (cookie / Basic-password / Basic-PAT),
	// so these routes deliberately skip RequireAuth. See git_http.go for the
	// auth flow. Both upload-pack (read) and receive-pack (write) are wired
	// through the same `info/refs` endpoint via the `service` query param.
	r.Get("/git/{owner}/{namegit}/info/refs", h.gitInfoRefs)
	r.Post("/git/{owner}/{namegit}/git-upload-pack", h.gitUploadPack)
	r.Post("/git/{owner}/{namegit}/git-receive-pack", h.gitReceivePack)
}

// publicRepo is the JSON projection. We mirror the DB fields one-to-one and
// expose owner_kind / owner_name (with owner_username kept as a kind-blind
// alias so the existing UI can continue to build clone URLs without caring
// whether the owner is a user or an org).
type publicRepo struct {
	ID               int64     `json:"id"`
	OwnerKind        string    `json:"owner_kind"`
	OwnerID          int64     `json:"owner_id"`
	OwnerName        string    `json:"owner_name"`
	OwnerUsername    string    `json:"owner_username"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	Visibility       string    `json:"visibility"`
	DefaultBranch    string    `json:"default_branch"`
	ViewerPermission string    `json:"viewer_permission,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func toPublic(r *domain.Repo) publicRepo {
	return publicRepo{
		ID:            r.ID,
		OwnerKind:     string(r.OwnerKind),
		OwnerID:       r.OwnerID,
		OwnerName:     r.OwnerName,
		OwnerUsername: r.OwnerName,
		Name:          r.Name,
		Description:   r.Description,
		Visibility:    string(r.Visibility),
		DefaultBranch: r.DefaultBranch,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}

// ---- CRUD ----

type createReq struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Visibility    string `json:"visibility"`
	DefaultBranch string `json:"default_branch,omitempty"`
	InitReadme    bool   `json:"init_readme,omitempty"`
	// Owner optionally selects the target owner namespace. Empty means
	// "the calling user"; a non-empty value must resolve to a user (the
	// caller themselves) or an org of which the caller is a member.
	Owner string `json:"owner,omitempty"`
	// AgentsYAML is an optional override for the seeded `.hangrix/
	// agents.yml`. Only consulted when InitReadme=true. Empty means
	// "use the bundled template verbatim". The handler parses the
	// body via agentsconfig.ParseHostConfig before writing — invalid
	// yaml short-circuits with 400 so we never seed a repo with a
	// config the runtime would reject on first spawn.
	AgentsYAML string `json:"agents_yaml,omitempty"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)

	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !repoNameRe.MatchString(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	visibility := domain.Visibility(strings.TrimSpace(req.Visibility))
	if visibility == "" {
		visibility = domain.VisibilityPrivate
	}
	if !visibility.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "invalid visibility")
		return
	}
	defaultBranch := strings.TrimSpace(req.DefaultBranch)
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	if !isValidBranchName(defaultBranch) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid default_branch")
		return
	}

	ctx := r.Context()
	ownerKind, ownerID, ownerName, ok := h.resolveCreateOwner(w, r, caller, strings.TrimSpace(req.Owner))
	if !ok {
		return
	}

	repo, err := h.store.Create(ctx, ownerKind, ownerID, req.Name, req.Description, defaultBranch, visibility)
	if err != nil {
		if errors.Is(err, domain.ErrRepoConflict) {
			httpx.WriteError(w, http.StatusConflict, "repo already exists")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// If the caller pasted a custom `.hangrix/agents.yml` body we
	// validate it (so a bad config can't land in the seed commit and
	// brick `agent spawn` later) then write it as the seed file. We
	// also write stub prompt files for any role keys the body refers
	// to that the bundled `templates/initial/.hangrix/prompts/`
	// doesn't already cover.
	var overrides map[string][]byte
	if req.InitReadme && strings.TrimSpace(req.AgentsYAML) != "" {
		files, err := prepareAgentFiles(req.AgentsYAML)
		if err != nil {
			_ = h.store.Delete(ctx, repo.ID)
			httpx.WriteError(w, http.StatusBadRequest, "agents.yml: "+err.Error())
			return
		}
		overrides = files
	}

	// Best-effort filesystem init. On failure we roll back the DB row so the
	// caller can retry with the same name; otherwise the metadata row would
	// orphan a missing bare repo. The seed commit's author identity is still
	// the calling user regardless of who ends up owning the repo.
	if err := h.storage.InitOnDisk(repo, ownerName, req.InitReadme, overrides, caller.Username, caller.Email); err != nil {
		_ = h.store.Delete(ctx, repo.ID)
		httpx.WriteError(w, http.StatusInternalServerError, "init repo: "+err.Error())
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, toPublic(repo))
}

// resolveCreateOwner inspects the optional req.Owner field and returns the
// (kind, id, name) tuple to create the repo under. The caller is the
// authoritative source for the "no owner specified → use my account" path.
// For an explicit owner: it must resolve to either the caller themselves or
// an org of which the caller is a member (any role — admin tightening is a
// follow-up). Anything else → 403/404 as appropriate, and ok=false.
func (h *Handler) resolveCreateOwner(w http.ResponseWriter, r *http.Request, caller *userdomain.User, ownerName string) (domain.OwnerKind, int64, string, bool) {
	if ownerName == "" || ownerName == caller.Username {
		return domain.OwnerKindUser, caller.ID, caller.Username, true
	}
	owner, err := h.resolver.ResolveOwner(r.Context(), ownerName)
	if err != nil {
		if errors.Is(err, orgdomain.ErrOwnerNotFound) || errors.Is(err, orgdomain.ErrOrgReserved) {
			httpx.WriteError(w, http.StatusNotFound, "owner not found")
			return "", 0, "", false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return "", 0, "", false
	}
	switch owner.Kind {
	case orgdomain.OwnerKindUser:
		// Only the caller themselves may create under a user namespace.
		if owner.ID != caller.ID {
			httpx.WriteError(w, http.StatusForbidden, "cannot create repo under another user")
			return "", 0, "", false
		}
		return domain.OwnerKindUser, owner.ID, owner.Name, true
	case orgdomain.OwnerKindOrg:
		_, ok, err := h.resolver.Membership(r.Context(), owner.ID, caller.ID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return "", 0, "", false
		}
		if !ok && caller.Role != userdomain.RoleAdmin {
			httpx.WriteError(w, http.StatusForbidden, "not an org member")
			return "", 0, "", false
		}
		return domain.OwnerKindOrg, owner.ID, owner.Name, true
	}
	httpx.WriteError(w, http.StatusInternalServerError, "unknown owner kind")
	return "", 0, "", false
}

type listResp struct {
	Items []publicRepo `json:"items"`
	Total int64        `json:"total"`
}

func (h *Handler) listMine(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)
	offset, limit := parseOffsetLimit(r)

	repos, total, err := h.store.ListByOwner(r.Context(), domain.OwnerKindUser, caller.ID, true, offset, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, listRespFrom(repos, total))
}

func (h *Handler) listByUsername(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	if !usernameRe.MatchString(username) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid username")
		return
	}
	owner, err := h.users.GetByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "user not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	caller, _ := authdomain.UserFromRequest(r)
	includePrivate := caller.ID == owner.ID || caller.Role == userdomain.RoleAdmin

	offset, limit := parseOffsetLimit(r)
	repos, total, err := h.store.ListByOwner(r.Context(), domain.OwnerKindUser, owner.ID, includePrivate, offset, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, listRespFrom(repos, total))
}

func (h *Handler) listByOrg(w http.ResponseWriter, r *http.Request) {
	orgName := chi.URLParam(r, "org")
	if !usernameRe.MatchString(orgName) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid org")
		return
	}
	org, err := h.orgs.GetByName(r.Context(), orgName)
	if err != nil {
		if errors.Is(err, orgdomain.ErrOrgNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "org not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	caller, _ := authdomain.UserFromRequest(r)
	_, isMember, err := h.resolver.Membership(r.Context(), org.ID, caller.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Members (or admin) get the full listing including private repos;
	// every other authenticated caller still sees the org but only its
	// public repos. Org rows themselves don't carry a visibility flag.
	includePrivate := isMember || caller.Role == userdomain.RoleAdmin

	offset, limit := parseOffsetLimit(r)
	repos, total, err := h.store.ListByOwner(r.Context(), domain.OwnerKindOrg, org.ID, includePrivate, offset, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, listRespFrom(repos, total))
}

func listRespFrom(repos []*domain.Repo, total int64) listResp {
	items := make([]publicRepo, 0, len(repos))
	for _, r := range repos {
		items = append(items, toPublic(r))
	}
	return listResp{Items: items, Total: total}
}

func (h *Handler) getOne(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	pr := toPublic(repo)
	caller, _ := authdomain.UserFromRequest(r)
	pr.ViewerPermission = h.viewerPermission(r.Context(), caller, repo)
	httpx.WriteJSON(w, http.StatusOK, pr)
}

type patchReq struct {
	Description   *string `json:"description,omitempty"`
	Visibility    *string `json:"visibility,omitempty"`
	DefaultBranch *string `json:"default_branch,omitempty"`
}

func (h *Handler) patchOne(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return
	}

	var req patchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	description := repo.Description
	if req.Description != nil {
		description = *req.Description
	}
	visibility := repo.Visibility
	if req.Visibility != nil {
		v := domain.Visibility(strings.TrimSpace(*req.Visibility))
		if !v.Valid() {
			httpx.WriteError(w, http.StatusBadRequest, "invalid visibility")
			return
		}
		visibility = v
	}
	defaultBranch := repo.DefaultBranch
	if req.DefaultBranch != nil {
		db := strings.TrimSpace(*req.DefaultBranch)
		if !isValidBranchName(db) {
			httpx.WriteError(w, http.StatusBadRequest, "invalid default_branch")
			return
		}
		// Keep DB metadata and the bare repo's HEAD symref in sync. Flip
		// HEAD first: if the branch doesn't exist we surface a 400 and
		// leave the DB row untouched. Only after the on-disk move
		// succeeds do we commit the metadata update.
		if db != repo.DefaultBranch {
			path, ok := h.resolveFsPath(w, repo)
			if !ok {
				return
			}
			if err := h.git.SetHEAD(path, db); err != nil {
				if errors.Is(err, gitdomain.ErrRefNotFound) {
					httpx.WriteError(w, http.StatusBadRequest, "default_branch does not exist as a branch")
					return
				}
				if mapGitErr(w, err) {
					return
				}
				httpx.WriteError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		defaultBranch = db
	}

	updated, err := h.store.UpdateMeta(r.Context(), repo.ID, description, defaultBranch, visibility)
	if err != nil {
		if errors.Is(err, domain.ErrRepoNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.DefaultBranch != nil && *req.DefaultBranch != repo.DefaultBranch {
		h.invalidateCache(r.Context(), repo.ID)
	}
	httpx.WriteJSON(w, http.StatusOK, toPublic(updated))
}

func (h *Handler) deleteOne(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return
	}
	if err := h.store.Delete(r.Context(), repo.ID); err != nil {
		if errors.Is(err, domain.ErrRepoNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// DB row is gone; remove the bare repo. Best-effort: log-equivalent is a
	// 500 only if removal failed in a way that isn't "already missing".
	if err := h.storage.DeleteOnDisk(repo.OwnerName, repo.Name); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "remove repo dir: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Transfer ----

type transferReq struct {
	TargetOwner string `json:"target_owner"`
	Confirm     string `json:"confirm"` // must equal "<owner>/<name>" of the source repo
}

// transfer moves a repo to a new owner namespace. Caller must already be
// allowed to write the source repo (owner / org-owner-role / admin), and
// must be a valid owner of the target (themselves, or an org owner of the
// target org). DB swap + on-disk rename happen sequentially; if the rename
// fails we roll the DB row back so a re-tried transfer can succeed.
func (h *Handler) transfer(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return
	}

	var req transferReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	target := strings.TrimSpace(req.TargetOwner)
	if !usernameRe.MatchString(target) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid target_owner")
		return
	}
	expectConfirm := repo.OwnerName + "/" + repo.Name
	if strings.TrimSpace(req.Confirm) != expectConfirm {
		httpx.WriteError(w, http.StatusBadRequest, "confirm must match '<owner>/<name>'")
		return
	}

	caller, _ := authdomain.UserFromRequest(r)
	owner, err := h.resolver.ResolveOwner(r.Context(), target)
	if err != nil {
		if errors.Is(err, orgdomain.ErrOwnerNotFound) || errors.Is(err, orgdomain.ErrOrgReserved) {
			httpx.WriteError(w, http.StatusNotFound, "target owner not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Caller must have rights to push into the target. Same rules as create.
	switch owner.Kind {
	case orgdomain.OwnerKindUser:
		if owner.ID != caller.ID && caller.Role != userdomain.RoleAdmin {
			httpx.WriteError(w, http.StatusForbidden, "cannot transfer to another user")
			return
		}
	case orgdomain.OwnerKindOrg:
		role, isMember, err := h.resolver.Membership(r.Context(), owner.ID, caller.ID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if (!isMember || role != orgdomain.RoleOwner) && caller.Role != userdomain.RoleAdmin {
			httpx.WriteError(w, http.StatusForbidden, "must be an org owner of the target")
			return
		}
	}

	newKind := domain.OwnerKind(owner.Kind)
	if repo.OwnerKind == newKind && repo.OwnerID == owner.ID {
		// No-op: same owner. Return the current repo so callers can treat
		// transfer as idempotent.
		httpx.WriteJSON(w, http.StatusOK, toPublic(repo))
		return
	}

	updated, err := h.store.Transfer(r.Context(), repo.ID, newKind, owner.ID)
	if err != nil {
		if errors.Is(err, domain.ErrRepoConflict) {
			httpx.WriteError(w, http.StatusConflict, "target already has a repo by that name")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.storage.RenameOnDisk(repo.OwnerName, repo.Name, owner.Name, repo.Name); err != nil {
		// Disk rename failed — undo the DB swap so the source location
		// remains the canonical truth. We swallow the rollback error
		// (logging not yet wired in this module); if rollback also fails
		// the admin can move the directory by hand and re-run transfer.
		_, _ = h.store.Transfer(r.Context(), repo.ID, repo.OwnerKind, repo.OwnerID)
		httpx.WriteError(w, http.StatusInternalServerError, "rename on disk: "+err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublic(updated))
}

// ---- Git reads ----

func (h *Handler) getRefs(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}

	// Try cache first; on miss fall through to git.
	cacheKey := kv.RefKey(repo.ID)
	var refs gitdomain.Refs
	if h.cache.Get(r.Context(), cacheKey, &refs) {
		httpx.WriteJSON(w, http.StatusOK, &refs)
		return
	}

	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}
	gitRefs, err := h.git.ListRefs(path)
	if err != nil {
		if mapGitErr(w, err) {
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.cache.Set(r.Context(), cacheKey, gitRefs, refsCacheTTL)
	httpx.WriteJSON(w, http.StatusOK, gitRefs)
}

func (h *Handler) listCommits(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	offset, limit := parseOffsetLimit(r)

	// Cache only the first page (offset=0), which is the common browser
	// scenario. Subsequent pages and custom offsets bypass the cache.
	if offset == 0 {
		cacheKey := kv.CommitsKey(repo.ID, ref, offset, limit)
		var cached []*gitdomain.Commit
		if h.cache.Get(r.Context(), cacheKey, &cached) {
			httpx.WriteJSON(w, http.StatusOK, cached)
			return
		}

		path, ok := h.resolveFsPath(w, repo)
		if !ok {
			return
		}
		commits, err := h.git.ListCommits(path, ref, int(offset), int(limit))
		if err != nil {
			if errors.Is(err, gitdomain.ErrEmptyRepo) {
				h.cache.Set(r.Context(), cacheKey, []*gitdomain.Commit{}, commitsCacheTTL)
				httpx.WriteJSON(w, http.StatusOK, []*gitdomain.Commit{})
				return
			}
			if mapGitErr(w, err) {
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.cache.Set(r.Context(), cacheKey, commits, commitsCacheTTL)
		httpx.WriteJSON(w, http.StatusOK, commits)
		return
	}

	// Non-first-page: no caching.
	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}
	commits, err := h.git.ListCommits(path, ref, int(offset), int(limit))
	if err != nil {
		if errors.Is(err, gitdomain.ErrEmptyRepo) {
			httpx.WriteJSON(w, http.StatusOK, []*gitdomain.Commit{})
			return
		}
		if mapGitErr(w, err) {
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, commits)
}

func (h *Handler) getCommit(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}
	sha := chi.URLParam(r, "sha")
	if sha == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing sha")
		return
	}
	cwd, err := h.git.CommitByID(path, sha)
	if err != nil {
		if mapGitErr(w, err) {
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, cwd)
}

func (h *Handler) getContainingRefs(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}
	sha := chi.URLParam(r, "sha")
	if sha == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing sha")
		return
	}
	refs, err := h.git.ContainsCommit(path, sha)
	if err != nil {
		if mapGitErr(w, err) {
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, refs)
}

// getArchive shells out to `git archive` to produce a zip or tar.gz of the
// requested ref. The captured path looks like "main.zip" or
// "release/v1.tar.gz" — we split off the extension, leaving the ref to
// resolve via the standard git grammar.
func (h *Handler) getArchive(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	fsPath, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}

	raw := strings.TrimSpace(chi.URLParam(r, "*"))
	if raw == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing ref")
		return
	}
	ref, format, contentType, ok := parseArchiveTarget(raw)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "unsupported archive format (use .zip or .tar.gz)")
		return
	}

	// `git archive` understands ref names and SHAs directly, but we still
	// validate the ref-name shape so a caller can't smuggle in shell-active
	// characters even though we go through exec.Command's argv.
	if !isSafeArchiveRef(ref) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid ref")
		return
	}

	// Prefix archive entries with "<repo>-<short>/" the way GitHub /
	// Gitea do, so unpacking is predictable. The shortRef helper trims
	// "refs/heads/" / "refs/tags/" prefixes if a caller passes a fully
	// qualified ref.
	prefix := repo.Name + "-" + shortRef(ref) + "/"
	filename := repo.Name + "-" + shortRef(ref) + "." + format

	cmd := exec.CommandContext(r.Context(),
		"git",
		"--git-dir="+fsPath,
		"archive",
		"--format="+format,
		"--prefix="+prefix,
		ref,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "archive: "+err.Error())
		return
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "archive: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	if _, err := io.Copy(w, stdout); err != nil {
		// We've already begun streaming, so we can't change the status code.
		// Best-effort cleanup; the underlying TCP error will surface to the
		// client.
		_ = cmd.Wait()
		return
	}
	_ = cmd.Wait()
}

func (h *Handler) getTree(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	treePath := r.URL.Query().Get("path")
	entries, err := h.git.Tree(path, ref, treePath)
	if err != nil {
		if errors.Is(err, gitdomain.ErrEmptyRepo) {
			httpx.WriteJSON(w, http.StatusOK, []*gitdomain.TreeEntry{})
			return
		}
		if mapGitErr(w, err) {
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, entries)
}

func (h *Handler) getTreeView(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	treePath := r.URL.Query().Get("path")

	// Try cache; the key includes repo + ref + path.
	cacheKey := kv.TreeViewKey(repo.ID, ref, treePath)
	var view gitdomain.TreeView
	if h.cache.Get(r.Context(), cacheKey, &view) {
		httpx.WriteJSON(w, http.StatusOK, &view)
		return
	}

	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}
	gitView, err := h.git.TreeView(path, ref, treePath)
	if err != nil {
		// TreeView returns a well-formed empty view on empty repos; any
		// error here is a real problem (bad ref, broken bare repo, etc.)
		if mapGitErr(w, err) {
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.cache.Set(r.Context(), cacheKey, gitView, treeViewCacheTTL)
	httpx.WriteJSON(w, http.StatusOK, gitView)
}

type blobResp struct {
	ContentBase64 string `json:"content_base64"`
	Binary        bool   `json:"binary"`
	Size          int    `json:"size"`
}

func (h *Handler) getBlob(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing path")
		return
	}
	content, binary, err := h.git.Blob(path, ref, filePath)
	if err != nil {
		if mapGitErr(w, err) {
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(content) > maxBlobBytes {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, blobResp{
		ContentBase64: base64.StdEncoding.EncodeToString(content),
		Binary:        binary,
		Size:          len(content),
	})
}

func (h *Handler) getDiff(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" || to == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing from/to")
		return
	}
	diffs, err := h.git.DiffRefs(path, from, to)
	if err != nil {
		if mapGitErr(w, err) {
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, diffs)
}

// ---- Helpers ----

// resolveRepoForRead loads the repo identified by the {owner}/{name} path
// segments and enforces read visibility. Returns false (and writes the HTTP
// error itself) on any failure.
func (h *Handler) resolveRepoForRead(w http.ResponseWriter, r *http.Request) (*domain.Repo, bool) {
	repo, ok := h.loadRepoFromPath(w, r)
	if !ok {
		return nil, false
	}
	caller, _ := authdomain.UserFromRequest(r)
	if repo.Visibility == domain.VisibilityPrivate {
		can, err := h.canReadRepo(r.Context(), caller, repo)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return nil, false
		}
		if !can {
			httpx.WriteError(w, http.StatusForbidden, "forbidden")
			return nil, false
		}
	}
	return repo, true
}

// resolveRepoForWrite enforces owner-or-admin authorization for mutating
// endpoints (PATCH / DELETE / branch and tag writes).
func (h *Handler) resolveRepoForWrite(w http.ResponseWriter, r *http.Request) (*domain.Repo, bool) {
	repo, ok := h.loadRepoFromPath(w, r)
	if !ok {
		return nil, false
	}
	caller, _ := authdomain.UserFromRequest(r)
	can, err := h.canWriteContents(r.Context(), caller, repo)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if !can {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return repo, true
}

// resolveRepoForManage enforces owner-or-admin authorization for management
// endpoints (PATCH metadata, DELETE, transfer, branch protections, members).
func (h *Handler) resolveRepoForManage(w http.ResponseWriter, r *http.Request) (*domain.Repo, bool) {
	repo, ok := h.loadRepoFromPath(w, r)
	if !ok {
		return nil, false
	}
	caller, _ := authdomain.UserFromRequest(r)
	can, err := h.canManageRepo(r.Context(), caller, repo)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if !can {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return repo, true
}

// canReadRepo: caller may read a (private) repo if they own it (user-owned),
// are a repo member (user-owned), are any-role member of the owning org,
// or are a platform admin.
func (h *Handler) canReadRepo(ctx context.Context, caller *userdomain.User, repo *domain.Repo) (bool, error) {
	if caller == nil {
		return false, nil
	}
	if caller.Role == userdomain.RoleAdmin {
		return true, nil
	}
	switch repo.OwnerKind {
	case domain.OwnerKindUser:
		if caller.ID == repo.OwnerID {
			return true, nil
		}
		// Check repo_members for user-owned repos: any role (read or write) grants read access.
		m, err := h.members.GetMember(ctx, repo.ID, caller.ID)
		if err != nil {
			if errors.Is(err, domain.ErrRepoMemberNotFound) {
				return false, nil
			}
			return false, err
		}
		return m.Role == domain.MemberRoleRead || m.Role == domain.MemberRoleWrite, nil
	case domain.OwnerKindOrg:
		_, ok, err := h.resolver.Membership(ctx, repo.OwnerID, caller.ID)
		return ok, err
	}
	return false, nil
}

// canManageRepo: caller may manage repo metadata (PATCH/DELETE/transfer),
// members, and branch protections. Only the repo owner or admin.
// For user-owned repos: the user owner. For org-owned repos: org owner.
func (h *Handler) canManageRepo(ctx context.Context, caller *userdomain.User, repo *domain.Repo) (bool, error) {
	if caller == nil {
		return false, nil
	}
	if caller.Role == userdomain.RoleAdmin {
		return true, nil
	}
	switch repo.OwnerKind {
	case domain.OwnerKindUser:
		return caller.ID == repo.OwnerID, nil
	case domain.OwnerKindOrg:
		role, ok, err := h.resolver.Membership(ctx, repo.OwnerID, caller.ID)
		if err != nil || !ok {
			return false, err
		}
		return role == orgdomain.RoleOwner, nil
	}
	return false, nil
}

// canWriteContents: caller may push and create/delete branches/tags.
// Owner/admin always; for user-owned repos, write members also qualify.
func (h *Handler) canWriteContents(ctx context.Context, caller *userdomain.User, repo *domain.Repo) (bool, error) {
	if caller == nil {
		return false, nil
	}
	if caller.Role == userdomain.RoleAdmin {
		return true, nil
	}
	switch repo.OwnerKind {
	case domain.OwnerKindUser:
		if caller.ID == repo.OwnerID {
			return true, nil
		}
		m, err := h.members.GetMember(ctx, repo.ID, caller.ID)
		if err != nil {
			if errors.Is(err, domain.ErrRepoMemberNotFound) {
				return false, nil
			}
			return false, err
		}
		return m.Role == domain.MemberRoleWrite, nil
	case domain.OwnerKindOrg:
		role, ok, err := h.resolver.Membership(ctx, repo.OwnerID, caller.ID)
		if err != nil || !ok {
			return false, err
		}
		return role == orgdomain.RoleOwner, nil
	}
	return false, nil
}

// viewerPermission returns the viewer's permission level for the frontend.
// Values: "manage", "write", "read", or "" (none).
func (h *Handler) viewerPermission(ctx context.Context, caller *userdomain.User, repo *domain.Repo) string {
	if caller == nil {
		return ""
	}
	if caller.Role == userdomain.RoleAdmin {
		return "manage"
	}
	switch repo.OwnerKind {
	case domain.OwnerKindUser:
		if caller.ID == repo.OwnerID {
			return "manage"
		}
		m, err := h.members.GetMember(ctx, repo.ID, caller.ID)
		if err != nil {
			return ""
		}
		return string(m.Role) // "write" or "read"
	case domain.OwnerKindOrg:
		role, ok, err := h.resolver.Membership(ctx, repo.OwnerID, caller.ID)
		if err != nil || !ok {
			return ""
		}
		if role == orgdomain.RoleOwner {
			return "manage"
		}
		return "read"
	}
	return ""
}

func (h *Handler) loadRepoFromPath(w http.ResponseWriter, r *http.Request) (*domain.Repo, bool) {
	ownerName := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "name")
	if !usernameRe.MatchString(ownerName) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid owner")
		return nil, false
	}
	if !repoNameRe.MatchString(repoName) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return nil, false
	}
	owner, err := h.resolver.ResolveOwner(r.Context(), ownerName)
	if err != nil {
		if errors.Is(err, orgdomain.ErrOwnerNotFound) || errors.Is(err, orgdomain.ErrOrgReserved) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	repo, err := h.store.GetByOwnerAndName(r.Context(), domain.OwnerKind(owner.Kind), owner.ID, repoName)
	if err != nil {
		if errors.Is(err, domain.ErrRepoNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return repo, true
}

func (h *Handler) resolveFsPath(w http.ResponseWriter, repo *domain.Repo) (string, bool) {
	path, err := h.storage.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		// This shouldn't happen — the names already passed handler-level
		// validation — but guard anyway since Storage owns the FS rules.
		httpx.WriteError(w, http.StatusInternalServerError, "resolve path: "+err.Error())
		return "", false
	}
	return path, true
}

// mapGitErr translates known git sentinels to HTTP status codes. Returns
// true if it wrote a response (caller should stop); false to let the caller
// fall through to a 500.
func mapGitErr(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, gitdomain.ErrRefNotFound):
		httpx.WriteError(w, http.StatusNotFound, "ref not found")
		return true
	case errors.Is(err, gitdomain.ErrPathNotFound):
		httpx.WriteError(w, http.StatusNotFound, "path not found")
		return true
	case errors.Is(err, gitdomain.ErrRepoNotFound):
		httpx.WriteError(w, http.StatusNotFound, "repo storage missing")
		return true
	case errors.Is(err, gitdomain.ErrNotABlob):
		httpx.WriteError(w, http.StatusBadRequest, "not a blob")
		return true
	case errors.Is(err, gitdomain.ErrBranchExists):
		httpx.WriteError(w, http.StatusConflict, "branch already exists")
		return true
	case errors.Is(err, gitdomain.ErrTagExists):
		httpx.WriteError(w, http.StatusConflict, "tag already exists")
		return true
	case errors.Is(err, gitdomain.ErrCannotDeleteHEAD):
		httpx.WriteError(w, http.StatusConflict, "cannot delete current HEAD branch")
		return true
	case errors.Is(err, gitdomain.ErrInvalidRefName):
		httpx.WriteError(w, http.StatusBadRequest, "invalid ref name")
		return true
	}
	return false
}

// isValidBranchName accepts a conservative subset of git ref rules. We don't
// need the full grammar; rejecting whitespace, control chars, and a few
// reserved sequences is enough to keep the field UI-safe.
func isValidBranchName(name string) bool {
	if name == "" || len(name) > 100 {
		return false
	}
	if strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "//") {
		return false
	}
	for _, r := range name {
		if r <= 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case '~', '^', ':', '?', '*', '[', '\\', ' ':
			return false
		}
	}
	return true
}

func parseOffsetLimit(r *http.Request) (offset, limit int32) {
	limit, offset = 50, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = int32(n)
		}
	}
	return
}

// ---- Branch / tag writes ----

type createBranchReq struct {
	Name     string `json:"name"`
	StartRef string `json:"start_ref"`
}

type refMutationResp struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

// validateRefNameInput is the shared input gate for branch/tag names coming
// off the wire. It rejects the most obvious abuse up front; the git module
// still applies the canonical ref-name grammar after this.
func validateRefNameInput(name string) bool {
	if name == "" || len(name) > 200 {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	return true
}

func (h *Handler) createBranch(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForWrite(w, r)
	if !ok {
		return
	}
	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}

	var req createBranchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(req.Name)
	start := strings.TrimSpace(req.StartRef)
	if !validateRefNameInput(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	if start == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing start_ref")
		return
	}

	startSHA, _ := h.git.ResolveCommit(path, start)
	if err := h.runGuards(r.Context(), domain.BranchWriteOp{
		RepoID:   repo.ID,
		Branch:   name,
		OldSHA:   "",
		NewSHA:   startSHA,
		IsCreate: true,
	}); err != nil {
		if errors.Is(err, domain.ErrBranchWriteDenied) {
			httpx.WriteError(w, http.StatusForbidden, err.Error())
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := h.git.CreateBranch(path, name, start); err != nil {
		if mapGitErr(w, err) {
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Look up the SHA the new ref points at so the client doesn't need a
	// follow-up GET /refs round-trip. We resolve via ListRefs to stay on
	// the public Git interface rather than reaching into go-git here.
	sha := lookupBranchSHA(h.git, path, name)
	h.invalidateCache(r.Context(), repo.ID)
	httpx.WriteJSON(w, http.StatusCreated, refMutationResp{Name: name, SHA: sha})
}

func (h *Handler) deleteBranch(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForWrite(w, r)
	if !ok {
		return
	}
	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}

	name := strings.TrimSpace(chi.URLParam(r, "*"))
	if name == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing branch")
		return
	}

	// Honor branch_protections.forbid_delete from the API side. The
	// receive-pack hook does the same check for git-CLI deletes; this
	// branch covers the web button.
	rule, err := h.matchedProtection(r, repo.ID, name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rule != nil && rule.ForbidDelete {
		httpx.WriteError(w, http.StatusConflict, "branch is protected against deletion")
		return
	}

	oldSHA, _ := h.git.ResolveCommit(path, name)
	if err := h.runGuards(r.Context(), domain.BranchWriteOp{
		RepoID:   repo.ID,
		Branch:   name,
		OldSHA:   oldSHA,
		NewSHA:   "",
		IsDelete: true,
	}); err != nil {
		if errors.Is(err, domain.ErrBranchWriteDenied) {
			httpx.WriteError(w, http.StatusForbidden, err.Error())
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := h.git.DeleteBranch(path, name); err != nil {
		if mapGitErr(w, err) {
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.invalidateCache(r.Context(), repo.ID)
	w.WriteHeader(http.StatusNoContent)
}

type createTagReq struct {
	Name      string `json:"name"`
	Ref       string `json:"ref"`
	Message   string `json:"message,omitempty"`
	Annotated bool   `json:"annotated,omitempty"`
}

func (h *Handler) createTag(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForWrite(w, r)
	if !ok {
		return
	}
	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}

	var req createTagReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(req.Name)
	ref := strings.TrimSpace(req.Ref)
	if !validateRefNameInput(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	if ref == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing ref")
		return
	}
	if req.Annotated && strings.TrimSpace(req.Message) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "annotated tag requires message")
		return
	}

	if req.Annotated {
		caller, _ := authdomain.UserFromRequest(r)
		sig := gitdomain.Signature{
			Name:  caller.Username,
			Email: caller.Email,
			When:  time.Now(),
		}
		if err := h.git.CreateAnnotatedTag(path, name, ref, req.Message, sig); err != nil {
			if mapGitErr(w, err) {
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if err := h.git.CreateLightweightTag(path, name, ref); err != nil {
			if mapGitErr(w, err) {
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	sha := lookupTagSHA(h.git, path, name)
	h.invalidateCache(r.Context(), repo.ID)
	httpx.WriteJSON(w, http.StatusCreated, refMutationResp{Name: name, SHA: sha})
}

func (h *Handler) deleteTag(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForWrite(w, r)
	if !ok {
		return
	}
	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}

	name := strings.TrimSpace(chi.URLParam(r, "*"))
	if name == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing tag")
		return
	}

	if err := h.git.DeleteTag(path, name); err != nil {
		if mapGitErr(w, err) {
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.invalidateCache(r.Context(), repo.ID)
	w.WriteHeader(http.StatusNoContent)
}

// lookupBranchSHA / lookupTagSHA fetch the SHA of a freshly-created ref via
// the public Git interface. Best-effort: on any failure we return "" and let
// the client GET /refs to recover — the ref was already written successfully.
func lookupBranchSHA(g gitdomain.Git, path, name string) string {
	refs, err := g.ListRefs(path)
	if err != nil {
		return ""
	}
	for _, b := range refs.Branches {
		if b.Name == name {
			return b.SHA
		}
	}
	return ""
}

func lookupTagSHA(g gitdomain.Git, path, name string) string {
	refs, err := g.ListRefs(path)
	if err != nil {
		return ""
	}
	for _, t := range refs.Tags {
		if t.Name == name {
			return t.SHA
		}
	}
	return ""
}

// ---- Archive helpers ----

// parseArchiveTarget splits a captured path like "main.zip" or
// "feature/x.tar.gz" into (ref, gitFormatName, contentType). Only .zip and
// .tar.gz are accepted — the two formats `git archive` ships out of the box
// and the two web clients actually want.
func parseArchiveTarget(raw string) (ref, format, contentType string, ok bool) {
	switch {
	case strings.HasSuffix(raw, ".tar.gz"):
		return strings.TrimSuffix(raw, ".tar.gz"), "tar.gz", "application/gzip", true
	case strings.HasSuffix(raw, ".zip"):
		return strings.TrimSuffix(raw, ".zip"), "zip", "application/zip", true
	}
	return "", "", "", false
}

// isSafeArchiveRef applies the same conservative gate as branch / tag names
// plus a "no leading dash" check (so a ref name can't be misread as a flag
// by `git archive` — argv-based exec means this is belt-and-braces, not
// load-bearing, but the cost is one line).
func isSafeArchiveRef(ref string) bool {
	if ref == "" || len(ref) > 200 {
		return false
	}
	if strings.HasPrefix(ref, "-") {
		return false
	}
	if strings.Contains(ref, "..") {
		return false
	}
	for _, r := range ref {
		if r <= 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case '~', '^', ':', '?', '*', '[', '\\', ' ', ';', '|', '&', '$', '`', '"', '\'':
			return false
		}
	}
	return true
}

// shortRef strips "refs/heads/" or "refs/tags/" if a fully qualified ref
// slipped through, so the archive filename stays readable.
func shortRef(ref string) string {
	for _, p := range []string{"refs/heads/", "refs/tags/", "refs/"} {
		if strings.HasPrefix(ref, p) {
			return strings.TrimPrefix(ref, p)
		}
	}
	return ref
}

// ---- Branch protection handlers ----

type publicProtection struct {
	ID               int64     `json:"id"`
	RepoID           int64     `json:"repo_id"`
	Pattern          string    `json:"pattern"`
	ForbidForcePush  bool      `json:"forbid_force_push"`
	ForbidDelete     bool      `json:"forbid_delete"`
	ForbidDirectPush bool      `json:"forbid_direct_push"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func toPublicProtection(p *domain.BranchProtection) publicProtection {
	return publicProtection{
		ID:               p.ID,
		RepoID:           p.RepoID,
		Pattern:          p.Pattern,
		ForbidForcePush:  p.ForbidForcePush,
		ForbidDelete:     p.ForbidDelete,
		ForbidDirectPush: p.ForbidDirectPush,
		CreatedAt:        p.CreatedAt,
		UpdatedAt:        p.UpdatedAt,
	}
}

type protectionReq struct {
	Pattern          string `json:"pattern"`
	ForbidForcePush  bool   `json:"forbid_force_push"`
	ForbidDelete     bool   `json:"forbid_delete"`
	ForbidDirectPush bool   `json:"forbid_direct_push"`
}

func (h *Handler) listProtections(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	rules, err := h.protections.List(r.Context(), repo.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]publicProtection, 0, len(rules))
	for _, p := range rules {
		out = append(out, toPublicProtection(p))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) createProtection(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return
	}
	var req protectionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	pattern := strings.TrimSpace(req.Pattern)
	if !isValidProtectionPattern(pattern) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid pattern")
		return
	}
	created, err := h.protections.Create(r.Context(), repo.ID, pattern, req.ForbidForcePush, req.ForbidDelete, req.ForbidDirectPush)
	if err != nil {
		if errors.Is(err, domain.ErrProtectionConflict) {
			httpx.WriteError(w, http.StatusConflict, "pattern already protected")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toPublicProtection(created))
}

func (h *Handler) updateProtection(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return
	}
	id, ok := parsePathID(w, r, "id")
	if !ok {
		return
	}
	var req protectionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	pattern := strings.TrimSpace(req.Pattern)
	if !isValidProtectionPattern(pattern) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid pattern")
		return
	}
	updated, err := h.protections.Update(r.Context(), id, repo.ID, pattern, req.ForbidForcePush, req.ForbidDelete, req.ForbidDirectPush)
	if err != nil {
		if errors.Is(err, domain.ErrProtectionNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "protection not found")
			return
		}
		if errors.Is(err, domain.ErrProtectionConflict) {
			httpx.WriteError(w, http.StatusConflict, "pattern already protected")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublicProtection(updated))
}

func (h *Handler) deleteProtection(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return
	}
	id, ok := parsePathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.protections.Delete(r.Context(), id, repo.ID); err != nil {
		if errors.Is(err, domain.ErrProtectionNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "protection not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isValidProtectionPattern accepts glob patterns suitable for path.Match.
// The set is the union of safe branch-name characters with the two glob
// metas we actually want: `*` and `?`. Square-bracket classes are not
// allowed — they're rarely worth the parsing risk and the user can always
// add separate rules.
func isValidProtectionPattern(p string) bool {
	if p == "" || len(p) > 200 {
		return false
	}
	if strings.HasPrefix(p, "-") || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	if strings.Contains(p, "..") || strings.Contains(p, "//") {
		return false
	}
	for _, r := range p {
		if r <= 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case '~', '^', ':', '[', '\\', ' ':
			return false
		}
	}
	return true
}

func parsePathID(w http.ResponseWriter, r *http.Request, param string) (int64, bool) {
	raw := chi.URLParam(r, param)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid "+param)
		return 0, false
	}
	return id, true
}

// matchedProtection returns the rule whose pattern matches branchName, or
// nil. Used by createBranch / deleteBranch / receive-pack to honor rules
// without each call re-doing the List → Match dance.
func (h *Handler) matchedProtection(r *http.Request, repoID int64, branchName string) (*domain.BranchProtection, error) {
	rules, err := h.protections.List(r.Context(), repoID)
	if err != nil {
		return nil, err
	}
	return domain.MatchProtection(rules, branchName), nil
}

// invalidateCache drops every cached git-read result for the given repo.
// Best-effort — failures are swallowed because stale reads are acceptable
// for the remaining TTL, and a Redis blip shouldn't fail the write.
func (h *Handler) invalidateCache(ctx context.Context, repoID int64) {
	h.cache.InvalidateRepo(ctx, repoID)
}

// runGuards walks every registered BranchWriteGuard. The first one that
// rejects short-circuits the chain. Returning ErrBranchWriteDenied (possibly
// wrapped) signals the caller to emit a 403; anything else is a 500.
func (h *Handler) runGuards(ctx context.Context, op domain.BranchWriteOp) error {
	for _, g := range h.guards {
		if err := g.CheckBranchWrite(ctx, op); err != nil {
			return err
		}
	}
	return nil
}

// ---- Repo members ----

type publicRepoMember struct {
	UserID   int64     `json:"user_id"`
	Username string    `json:"username"`
	Role     string    `json:"role"`
	AddedAt  time.Time `json:"added_at"`
	AddedBy  int64     `json:"added_by"`
}

func toPublicRepoMember(m *domain.RepoMember) publicRepoMember {
	return publicRepoMember{
		UserID:   m.UserID,
		Username: m.Username,
		Role:     string(m.Role),
		AddedAt:  m.AddedAt,
		AddedBy:  m.AddedBy,
	}
}

// resolveRepoForMembers loads the repo and gates on: must be user-owned,
// caller must be owner/admin. Returns the repo. Writes HTTP errors itself.
func (h *Handler) resolveRepoForMembers(w http.ResponseWriter, r *http.Request) (*domain.Repo, bool) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return nil, false
	}
	if repo.OwnerKind != domain.OwnerKindUser {
		httpx.WriteError(w, http.StatusBadRequest, "repo members are only supported on user-owned repos")
		return nil, false
	}
	return repo, true
}

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	if repo.OwnerKind != domain.OwnerKindUser {
		httpx.WriteError(w, http.StatusBadRequest, "repo members are only supported on user-owned repos")
		return
	}
	members, err := h.members.ListMembers(r.Context(), repo.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]publicRepoMember, 0, len(members))
	for _, m := range members {
		out = append(out, toPublicRepoMember(m))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

type addRepoMemberReq struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

func (h *Handler) addMember(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForMembers(w, r)
	if !ok {
		return
	}

	var req addRepoMemberReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !usernameRe.MatchString(req.Username) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid username")
		return
	}
	role := domain.MemberRole(strings.TrimSpace(req.Role))
	if role == "" {
		role = domain.MemberRoleRead
	}
	if !role.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "invalid role")
		return
	}

	target, err := h.users.GetByUsername(r.Context(), req.Username)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "user not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Don't allow adding the repo owner as a member.
	if target.ID == repo.OwnerID {
		httpx.WriteError(w, http.StatusBadRequest, "cannot add the repo owner as a member")
		return
	}

	caller, _ := authdomain.UserFromRequest(r)
	if err := h.members.AddMember(r.Context(), repo.ID, target.ID, caller.ID, role); err != nil {
		if errors.Is(err, domain.ErrRepoMemberConflict) {
			httpx.WriteError(w, http.StatusConflict, "already a member")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	m, err := h.members.GetMember(r.Context(), repo.ID, target.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toPublicRepoMember(m))
}

type patchRepoMemberReq struct {
	Role string `json:"role"`
}

func (h *Handler) patchMember(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForMembers(w, r)
	if !ok {
		return
	}
	target, ok := h.loadMemberUser(w, r)
	if !ok {
		return
	}

	var req patchRepoMemberReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	role := domain.MemberRole(strings.TrimSpace(req.Role))
	if !role.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "invalid role")
		return
	}

	current, err := h.members.GetMember(r.Context(), repo.ID, target.ID)
	if err != nil {
		if errors.Is(err, domain.ErrRepoMemberNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "member not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if current.Role == role {
		// No-op: return current state.
		httpx.WriteJSON(w, http.StatusOK, toPublicRepoMember(current))
		return
	}

	if err := h.members.UpdateMemberRole(r.Context(), repo.ID, target.ID, role); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	m, err := h.members.GetMember(r.Context(), repo.ID, target.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublicRepoMember(m))
}

func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	if repo.OwnerKind != domain.OwnerKindUser {
		httpx.WriteError(w, http.StatusBadRequest, "repo members are only supported on user-owned repos")
		return
	}
	target, ok := h.loadMemberUser(w, r)
	if !ok {
		return
	}

	caller, _ := authdomain.UserFromRequest(r)
	// A user may always remove themselves; otherwise it's owner-only.
	if caller.ID != target.ID {
		can, err := h.canManageRepo(r.Context(), caller, repo)
		if err != nil || !can {
			httpx.WriteError(w, http.StatusForbidden, "forbidden")
			return
		}
	}

	if err := h.members.RemoveMember(r.Context(), repo.ID, target.ID); err != nil {
		if errors.Is(err, domain.ErrRepoMemberNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "member not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// loadMemberUser looks up a user by the {username} URL param.
func (h *Handler) loadMemberUser(w http.ResponseWriter, r *http.Request) (*userdomain.User, bool) {
	username := chi.URLParam(r, "username")
	if !usernameRe.MatchString(username) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid username")
		return nil, false
	}
	u, err := h.users.GetByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "user not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return u, true
}

// ---- Repo variables ----

// variableItem is the public shape of a plain variable.
// Mirrors apps/web/app/types/repo.ts:RepoVariable.
type variableItem struct {
	Name      string    `json:"name"`
	Value     string    `json:"value,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// secretMeta is the public shape of a secret variable (value never exposed).
// Mirrors apps/web/app/types/repo.ts:RepoSecretMeta.
type secretMeta struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// variableListResp matches the frontend RepoVariableListResp contract.
type variableListResp struct {
	Variables []variableItem `json:"variables"`
	Secrets   []secretMeta   `json:"secrets"`
}

	// variableResp is the response shape for POST/PATCH on a single variable.
	// The frontend re-fetches the list after mutations so the exact shape is
	// not critical, but it must include name, value (plain only), kind, and
	// timestamps.
	type variableResp struct {
		Name      string    `json:"name"`
		Value     string    `json:"value,omitempty"`
		Kind      string    `json:"kind"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}

	func toVariableResp(v *domain.RepoVariable) variableResp {
		r := variableResp{
			Name:      v.Name,
			Kind:      string(v.Kind),
			CreatedAt: v.CreatedAt,
			UpdatedAt: v.UpdatedAt,
		}
		if v.Kind == domain.VariableKindPlain {
			r.Value = v.Value
		}
		return r
	}


func (h *Handler) listVariables(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return
	}
	vars, err := h.variables.List(r.Context(), repo.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var out variableListResp
	for _, v := range vars {
		if v.Kind == domain.VariableKindSecret {
			out.Secrets = append(out.Secrets, secretMeta{
				Name:      v.Name,
				CreatedAt: v.CreatedAt,
				UpdatedAt: v.UpdatedAt,
			})
		} else {
			out.Variables = append(out.Variables, variableItem{
				Name:      v.Name,
				Value:     v.Value,
				CreatedAt: v.CreatedAt,
				UpdatedAt: v.UpdatedAt,
			})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

type variableCreateReq struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Kind  string `json:"kind"` // "plain" or "secret"
}

func (h *Handler) createVariable(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return
	}

	var req variableCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Value = strings.TrimSpace(req.Value)
	kind := domain.VariableKind(strings.TrimSpace(req.Kind))
	if kind == "" {
		kind = domain.VariableKindPlain
	}
	if req.Name == "" {
		httpx.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Value == "" {
		httpx.WriteError(w, http.StatusBadRequest, "value is required")
		return
	}

	vr, err := h.variables.Create(r.Context(), repo.ID, req.Name, req.Value, kind)
	if err != nil {
		if errors.Is(err, domain.ErrVariableConflict) {
			httpx.WriteError(w, http.StatusConflict, "variable already exists")
			return
		}
		if errors.Is(err, domain.ErrVariableNameInvalid) || errors.Is(err, domain.ErrVariableNameEmpty) {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, domain.ErrVariableKindInvalid) {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toVariableResp(vr))
}

type variableUpdateReq struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Kind  string `json:"kind"`
}

func (h *Handler) updateVariable(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return
	}
	varName := chi.URLParam(r, "varName")
	if varName == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing variable name")
		return
	}

	// Lookup existing variable by name to get its ID.
	vars, err := h.variables.List(r.Context(), repo.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var existing *domain.RepoVariable
	for _, v := range vars {
		if v.Name == varName {
			existing = v
			break
		}
	}
	if existing == nil {
		httpx.WriteError(w, http.StatusNotFound, "variable not found")
		return
	}

	var req variableUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	newName := strings.TrimSpace(req.Name)
	if newName == "" {
		newName = existing.Name
	}
	newValue := strings.TrimSpace(req.Value)
	newKind := existing.Kind
	if k := domain.VariableKind(strings.TrimSpace(req.Kind)); k.Valid() {
		newKind = k
	}

	// For secrets: an empty value means "keep current encrypted value".
	// We must re-encrypt, so we need the plaintext. The variable store's
	// Get returns plaintext, which we can re-encrypt.
	if newKind == domain.VariableKindSecret && newValue == "" {
		// Re-fetch the current plaintext value.
		current, err := h.variables.Get(r.Context(), existing.ID, repo.ID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		newValue = current.Value
	}

	vr, err := h.variables.Update(r.Context(), existing.ID, repo.ID, newName, newValue, newKind)
	if err != nil {
		if errors.Is(err, domain.ErrVariableNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "variable not found")
			return
		}
		if errors.Is(err, domain.ErrVariableConflict) {
			httpx.WriteError(w, http.StatusConflict, "variable already exists")
			return
		}
		if errors.Is(err, domain.ErrVariableNameInvalid) || errors.Is(err, domain.ErrVariableNameEmpty) {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, domain.ErrVariableKindInvalid) {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toVariableResp(vr))
}

func (h *Handler) deleteVariable(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return
	}
	varName := chi.URLParam(r, "varName")
	if varName == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing variable name")
		return
	}

	vars, err := h.variables.List(r.Context(), repo.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var targetID int64
	for _, v := range vars {
		if v.Name == varName {
			targetID = v.ID
			break
		}
	}
	if targetID == 0 {
		httpx.WriteError(w, http.StatusNotFound, "variable not found")
		return
	}

	if err := h.variables.Delete(r.Context(), targetID, repo.ID); err != nil {
		if errors.Is(err, domain.ErrVariableNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "variable not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

