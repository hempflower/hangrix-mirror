// Package handler exposes the repo module's HTTP surface: CRUD over
// repository metadata plus read-only git endpoints (refs, commits, tree,
// blob, diff). Authorization is enforced here (not in SQL): public repos are
// visible to any authenticated user; private repos are visible only to the
// owner or an admin.
package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra"
	tokendomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

// maxBlobBytes caps blob responses. The HTTP layer refuses to return a file
// larger than this (clients should hit a future raw-streaming endpoint for
// big files) — 1 MiB is plenty for source code; larger payloads bloat JSON.
const maxBlobBytes = 1 << 20 // 1 MiB

// repoNameRe is the canonical repo-name regex. Must start with an
// alphanumeric or underscore, then up to 99 more chars from a slightly wider
// class. Mirrors the filesystem-safety set so a valid name is always a valid
// path component.
var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)

// usernameRe is reused for the {owner} path segment so we can fail fast on
// obviously bad input before hitting the database.
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)

type Handler struct {
	store      domain.Store
	storage    *infra.Storage
	git        gitdomain.Git
	users      userdomain.Repo
	tokens     tokendomain.Validator
	middleware authdomain.Middleware
}

type HandlerDeps struct {
	Store      domain.Store
	Storage    *infra.Storage
	Git        gitdomain.Git
	Users      userdomain.Repo
	Tokens     tokendomain.Validator
	Middleware authdomain.Middleware
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		store:      deps.Store,
		storage:    deps.Storage,
		git:        deps.Git,
		users:      deps.Users,
		tokens:     deps.Tokens,
		middleware: deps.Middleware,
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/repos", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Post("/", h.create)
		r.Get("/me", h.listMine)
		r.Get("/{owner}/{name}", h.getOne)
		r.Patch("/{owner}/{name}", h.patchOne)
		r.Delete("/{owner}/{name}", h.deleteOne)

		r.Get("/{owner}/{name}/refs", h.getRefs)
		r.Get("/{owner}/{name}/commits", h.listCommits)
		r.Get("/{owner}/{name}/commits/{sha}", h.getCommit)
		r.Get("/{owner}/{name}/tree", h.getTree)
		r.Get("/{owner}/{name}/tree-view", h.getTreeView)
		r.Get("/{owner}/{name}/blob", h.getBlob)
		r.Get("/{owner}/{name}/diff", h.getDiff)

		// Branch / tag write operations. The DELETE routes use chi's
		// trailing-wildcard pattern so names containing "/" (e.g.
		// "feature/foo") round-trip correctly through the URL path.
		r.Post("/{owner}/{name}/branches", h.createBranch)
		r.Delete("/{owner}/{name}/branches/*", h.deleteBranch)
		r.Post("/{owner}/{name}/tags", h.createTag)
		r.Delete("/{owner}/{name}/tags/*", h.deleteTag)
	})

	r.Route("/api/users/{username}/repos", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/", h.listByUsername)
	})

	// Smart HTTP. Auth is handled inline (cookie / Basic-password / Basic-PAT),
	// so these routes deliberately skip RequireAuth. See git_http.go for the
	// auth flow. Both upload-pack (read) and receive-pack (write) are wired
	// through the same `info/refs` endpoint via the `service` query param.
	r.Get("/git/{owner}/{namegit}/info/refs", h.gitInfoRefs)
	r.Post("/git/{owner}/{namegit}/git-upload-pack", h.gitUploadPack)
	r.Post("/git/{owner}/{namegit}/git-receive-pack", h.gitReceivePack)
}

// publicRepo is the JSON projection. We mirror the DB fields one-to-one and
// add owner_username so the client never has to do a second lookup to build
// a clone URL.
type publicRepo struct {
	ID            int64     `json:"id"`
	OwnerID       int64     `json:"owner_id"`
	OwnerUsername string    `json:"owner_username"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Visibility    string    `json:"visibility"`
	DefaultBranch string    `json:"default_branch"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func toPublic(r *domain.Repo) publicRepo {
	return publicRepo{
		ID:            r.ID,
		OwnerID:       r.OwnerID,
		OwnerUsername: r.OwnerUsername,
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
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)

	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !repoNameRe.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid name")
		return
	}
	visibility := domain.Visibility(strings.TrimSpace(req.Visibility))
	if visibility == "" {
		visibility = domain.VisibilityPrivate
	}
	if !visibility.Valid() {
		writeError(w, http.StatusBadRequest, "invalid visibility")
		return
	}
	defaultBranch := strings.TrimSpace(req.DefaultBranch)
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	if !isValidBranchName(defaultBranch) {
		writeError(w, http.StatusBadRequest, "invalid default_branch")
		return
	}

	ctx := r.Context()
	repo, err := h.store.Create(ctx, caller.ID, req.Name, req.Description, defaultBranch, visibility)
	if err != nil {
		if errors.Is(err, domain.ErrRepoConflict) {
			writeError(w, http.StatusConflict, "repo already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Best-effort filesystem init. On failure we roll back the DB row so the
	// caller can retry with the same name; otherwise the metadata row would
	// orphan a missing bare repo.
	if err := h.storage.InitOnDisk(repo, caller.Username, req.InitReadme, caller.Username, caller.Email); err != nil {
		_ = h.store.Delete(ctx, repo.ID)
		writeError(w, http.StatusInternalServerError, "init repo: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, toPublic(repo))
}

type listResp struct {
	Items []publicRepo `json:"items"`
	Total int64        `json:"total"`
}

func (h *Handler) listMine(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)
	offset, limit := parseOffsetLimit(r)

	repos, total, err := h.store.ListByOwner(r.Context(), caller.ID, true, offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, listRespFrom(repos, total))
}

func (h *Handler) listByUsername(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	if !usernameRe.MatchString(username) {
		writeError(w, http.StatusBadRequest, "invalid username")
		return
	}
	owner, err := h.users.GetByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	caller, _ := authdomain.UserFromRequest(r)
	includePrivate := caller.ID == owner.ID || caller.Role == userdomain.RoleAdmin

	offset, limit := parseOffsetLimit(r)
	repos, total, err := h.store.ListByOwner(r.Context(), owner.ID, includePrivate, offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, listRespFrom(repos, total))
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
	writeJSON(w, http.StatusOK, toPublic(repo))
}

type patchReq struct {
	Description   *string `json:"description,omitempty"`
	Visibility    *string `json:"visibility,omitempty"`
	DefaultBranch *string `json:"default_branch,omitempty"`
}

func (h *Handler) patchOne(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForWrite(w, r)
	if !ok {
		return
	}

	var req patchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
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
			writeError(w, http.StatusBadRequest, "invalid visibility")
			return
		}
		visibility = v
	}
	defaultBranch := repo.DefaultBranch
	if req.DefaultBranch != nil {
		db := strings.TrimSpace(*req.DefaultBranch)
		if !isValidBranchName(db) {
			writeError(w, http.StatusBadRequest, "invalid default_branch")
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
					writeError(w, http.StatusBadRequest, "default_branch does not exist as a branch")
					return
				}
				if mapGitErr(w, err) {
					return
				}
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		defaultBranch = db
	}

	updated, err := h.store.UpdateMeta(r.Context(), repo.ID, description, defaultBranch, visibility)
	if err != nil {
		if errors.Is(err, domain.ErrRepoNotFound) {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toPublic(updated))
}

func (h *Handler) deleteOne(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForWrite(w, r)
	if !ok {
		return
	}
	if err := h.store.Delete(r.Context(), repo.ID); err != nil {
		if errors.Is(err, domain.ErrRepoNotFound) {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// DB row is gone; remove the bare repo. Best-effort: log-equivalent is a
	// 500 only if removal failed in a way that isn't "already missing".
	if err := h.storage.DeleteOnDisk(repo.OwnerUsername, repo.Name); err != nil {
		writeError(w, http.StatusInternalServerError, "remove repo dir: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Git reads ----

func (h *Handler) getRefs(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}
	path, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}
	refs, err := h.git.ListRefs(path)
	if err != nil {
		if mapGitErr(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, refs)
}

func (h *Handler) listCommits(w http.ResponseWriter, r *http.Request) {
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
	offset, limit := parseOffsetLimit(r)
	commits, err := h.git.ListCommits(path, ref, int(offset), int(limit))
	if err != nil {
		// Empty repo is a normal state, not a 4xx — surface an empty list.
		if errors.Is(err, gitdomain.ErrEmptyRepo) {
			writeJSON(w, http.StatusOK, []*gitdomain.Commit{})
			return
		}
		if mapGitErr(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, commits)
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
		writeError(w, http.StatusBadRequest, "missing sha")
		return
	}
	cwd, err := h.git.CommitByID(path, sha)
	if err != nil {
		if mapGitErr(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cwd)
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
			writeJSON(w, http.StatusOK, []*gitdomain.TreeEntry{})
			return
		}
		if mapGitErr(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *Handler) getTreeView(w http.ResponseWriter, r *http.Request) {
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
	view, err := h.git.TreeView(path, ref, treePath)
	if err != nil {
		// TreeView returns a well-formed empty view on empty repos; any
		// error here is a real problem (bad ref, broken bare repo, etc.)
		if mapGitErr(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
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
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	content, binary, err := h.git.Blob(path, ref, filePath)
	if err != nil {
		if mapGitErr(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(content) > maxBlobBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}
	writeJSON(w, http.StatusOK, blobResp{
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
		writeError(w, http.StatusBadRequest, "missing from/to")
		return
	}
	diffs, err := h.git.DiffRefs(path, from, to)
	if err != nil {
		if mapGitErr(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, diffs)
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
		if caller.ID != repo.OwnerID && caller.Role != userdomain.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden")
			return nil, false
		}
	}
	return repo, true
}

// resolveRepoForWrite enforces owner-or-admin authorization for mutating
// endpoints (PATCH / DELETE).
func (h *Handler) resolveRepoForWrite(w http.ResponseWriter, r *http.Request) (*domain.Repo, bool) {
	repo, ok := h.loadRepoFromPath(w, r)
	if !ok {
		return nil, false
	}
	caller, _ := authdomain.UserFromRequest(r)
	if caller.ID != repo.OwnerID && caller.Role != userdomain.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return repo, true
}

func (h *Handler) loadRepoFromPath(w http.ResponseWriter, r *http.Request) (*domain.Repo, bool) {
	ownerName := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "name")
	if !usernameRe.MatchString(ownerName) {
		writeError(w, http.StatusBadRequest, "invalid owner")
		return nil, false
	}
	if !repoNameRe.MatchString(repoName) {
		writeError(w, http.StatusBadRequest, "invalid name")
		return nil, false
	}
	owner, err := h.users.GetByUsername(r.Context(), ownerName)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	repo, err := h.store.GetByOwnerAndName(r.Context(), owner.ID, repoName)
	if err != nil {
		if errors.Is(err, domain.ErrRepoNotFound) {
			writeError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return repo, true
}

func (h *Handler) resolveFsPath(w http.ResponseWriter, repo *domain.Repo) (string, bool) {
	path, err := h.storage.ResolvePath(repo.OwnerUsername, repo.Name)
	if err != nil {
		// This shouldn't happen — the names already passed handler-level
		// validation — but guard anyway since Storage owns the FS rules.
		writeError(w, http.StatusInternalServerError, "resolve path: "+err.Error())
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
		writeError(w, http.StatusNotFound, "ref not found")
		return true
	case errors.Is(err, gitdomain.ErrPathNotFound):
		writeError(w, http.StatusNotFound, "path not found")
		return true
	case errors.Is(err, gitdomain.ErrRepoNotFound):
		writeError(w, http.StatusNotFound, "repo storage missing")
		return true
	case errors.Is(err, gitdomain.ErrNotABlob):
		writeError(w, http.StatusBadRequest, "not a blob")
		return true
	case errors.Is(err, gitdomain.ErrBranchExists):
		writeError(w, http.StatusConflict, "branch already exists")
		return true
	case errors.Is(err, gitdomain.ErrTagExists):
		writeError(w, http.StatusConflict, "tag already exists")
		return true
	case errors.Is(err, gitdomain.ErrCannotDeleteHEAD):
		writeError(w, http.StatusConflict, "cannot delete current HEAD branch")
		return true
	case errors.Is(err, gitdomain.ErrInvalidRefName):
		writeError(w, http.StatusBadRequest, "invalid ref name")
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
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
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(req.Name)
	start := strings.TrimSpace(req.StartRef)
	if !validateRefNameInput(name) {
		writeError(w, http.StatusBadRequest, "invalid name")
		return
	}
	if start == "" {
		writeError(w, http.StatusBadRequest, "missing start_ref")
		return
	}

	if err := h.git.CreateBranch(path, name, start); err != nil {
		if mapGitErr(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Look up the SHA the new ref points at so the client doesn't need a
	// follow-up GET /refs round-trip. We resolve via ListRefs to stay on
	// the public Git interface rather than reaching into go-git here.
	sha := lookupBranchSHA(h.git, path, name)
	writeJSON(w, http.StatusCreated, refMutationResp{Name: name, SHA: sha})
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
		writeError(w, http.StatusBadRequest, "missing branch")
		return
	}

	if err := h.git.DeleteBranch(path, name); err != nil {
		if mapGitErr(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(req.Name)
	ref := strings.TrimSpace(req.Ref)
	if !validateRefNameInput(name) {
		writeError(w, http.StatusBadRequest, "invalid name")
		return
	}
	if ref == "" {
		writeError(w, http.StatusBadRequest, "missing ref")
		return
	}
	if req.Annotated && strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusBadRequest, "annotated tag requires message")
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
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if err := h.git.CreateLightweightTag(path, name, ref); err != nil {
			if mapGitErr(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	sha := lookupTagSHA(h.git, path, name)
	writeJSON(w, http.StatusCreated, refMutationResp{Name: name, SHA: sha})
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
		writeError(w, http.StatusBadRequest, "missing tag")
		return
	}

	if err := h.git.DeleteTag(path, name); err != nil {
		if mapGitErr(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
