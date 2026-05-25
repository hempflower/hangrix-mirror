// Package handler exposes the release module's HTTP surface under
// /api/repos/{owner}/{name}/releases.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	releasedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/release/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/release/infra"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
	workflowdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
)

// Handler contains the deps the release REST endpoints need.
type Handler struct {
	store            releasedomain.Store
	assets           releasedomain.AssetStore
	storage          *infra.AssetStorage
	git              gitdomain.Git
	repos            repodomain.Store
	resolver         repodomain.PathResolver
	orgResolver      orgdomain.Resolver
	middleware       authdomain.Middleware
	wfTokenValidator workflowdomain.WorkflowTokenValidator
}

type HandlerDeps struct {
	Store            releasedomain.Store
	Assets           releasedomain.AssetStore
	Storage          *infra.AssetStorage
	Git              gitdomain.Git
	Repos            repodomain.Store
	Resolver         repodomain.PathResolver
	OrgResolver      orgdomain.Resolver
	Middleware       authdomain.Middleware
	WfTokenValidator workflowdomain.WorkflowTokenValidator
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		store:            deps.Store,
		assets:           deps.Assets,
		storage:          deps.Storage,
		git:              deps.Git,
		repos:            deps.Repos,
		resolver:         deps.Resolver,
		orgResolver:      deps.OrgResolver,
		middleware:       deps.Middleware,
		wfTokenValidator: deps.WfTokenValidator,
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/repos/{owner}/{name}/releases", func(r chi.Router) {
		r.Use(h.authGate)
		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Get("/{id}", h.get)
		r.Patch("/{id}", h.update)
		r.Delete("/{id}", h.delete)
		r.Post("/{id}/publish", h.publish)
		r.Post("/{id}/assets", h.uploadAsset)
		r.Delete("/{id}/assets/{assetID}", h.deleteAsset)
		r.Get("/{id}/assets/{assetID}/download", h.downloadAsset)
	})
}

// authGate is a middleware that tries workflow token auth first, then falls
// back to cookie-based session auth. This allows workflow containers to call
// the release API with a Bearer token without needing a session cookie.
func (h *Handler) authGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Try workflow token (Bearer hangrix_wf_...).
		if h.wfTokenValidator != nil {
			tok, err := bearerTokenFromHeader(r)
			if err == nil && strings.HasPrefix(tok, "hangrix_wf_") {
				if _, err := h.wfTokenValidator.ValidateWorkflowToken(r.Context(), tok); err == nil {
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		// 2. Fall back to cookie auth.
		h.middleware.RequireAuth(next).ServeHTTP(w, r)
	})
}

// ---- JSON shapes ----

type publicRelease struct {
	ID              int64           `json:"id"`
	RepoID          int64           `json:"repo_id"`
	TagName         string          `json:"tag_name"`
	TargetCommitSHA string          `json:"target_commit_sha"`
	Title           string          `json:"title"`
	Notes           string          `json:"notes"`
	IsDraft         bool            `json:"is_draft"`
	PublishedAt     *time.Time      `json:"published_at"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	SourceArchives  []sourceArchive `json:"source_archives"`
	Assets          []publicAsset   `json:"assets"`
}

type sourceArchive struct {
	Format string `json:"format"`
	URL    string `json:"url"`
}

type publicAsset struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	CreatedAt   time.Time `json:"created_at"`
	DownloadURL string    `json:"download_url"`
}

type listResp struct {
	Items []publicRelease `json:"items"`
	Total int64           `json:"total"`
}

func toPublicRelease(repo *repodomain.Repo, rel *releasedomain.Release, assets []*releasedomain.Asset) publicRelease {
	base := fmt.Sprintf("/api/repos/%s/%s/releases", repo.OwnerName, repo.Name)
	pr := publicRelease{
		ID:              rel.ID,
		RepoID:          rel.RepoID,
		TagName:         rel.TagName,
		TargetCommitSHA: rel.TargetCommitSHA,
		Title:           rel.Title,
		Notes:           rel.Notes,
		IsDraft:         rel.IsDraft,
		CreatedAt:       rel.CreatedAt,
		UpdatedAt:       rel.UpdatedAt,
		SourceArchives: []sourceArchive{
			{Format: "zip", URL: fmt.Sprintf("/api/repos/%s/%s/archive/%s.zip", repo.OwnerName, repo.Name, rel.TagName)},
			{Format: "tar.gz", URL: fmt.Sprintf("/api/repos/%s/%s/archive/%s.tar.gz", repo.OwnerName, repo.Name, rel.TagName)},
		},
	}
	if !rel.PublishedAt.IsZero() {
		t := rel.PublishedAt
		pr.PublishedAt = &t
	}
	pas := make([]publicAsset, 0, len(assets))
	for _, a := range assets {
		pas = append(pas, publicAsset{
			ID:          a.ID,
			Name:        a.Name,
			ContentType: a.ContentType,
			SizeBytes:   a.SizeBytes,
			CreatedAt:   a.CreatedAt,
			DownloadURL: fmt.Sprintf("%s/%d/assets/%d/download", base, rel.ID, a.ID),
		})
	}
	pr.Assets = pas
	return pr
}

// ---- List ----

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r)
	if !ok {
		return
	}
	offset, limit := parseOffsetLimit(r)

	var draft *bool
	if v := r.URL.Query().Get("draft"); v != "" {
		b := v == "true" || v == "1"
		draft = &b
	}

	rels, total, err := h.store.ListByRepo(r.Context(), repo.ID, offset, limit, draft)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]publicRelease, 0, len(rels))
	for _, rel := range rels {
		assets, _ := h.assets.ListByRelease(r.Context(), rel.ID)
		items = append(items, toPublicRelease(repo, rel, assets))
	}
	httpx.WriteJSON(w, http.StatusOK, listResp{Items: items, Total: total})
}

// ---- Create ----

type createReq struct {
	TagName string `json:"tag_name"`
	Title   string `json:"title"`
	Notes   string `json:"notes,omitempty"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r)
	if !ok {
		return
	}
	if !h.checkWrite(w, r, repo) {
		return
	}

	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.TagName = strings.TrimSpace(req.TagName)
	if req.TagName == "" {
		httpx.WriteError(w, http.StatusBadRequest, "tag_name is required")
		return
	}
	if !isSafeRefName(req.TagName) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid tag_name")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		req.Title = req.TagName
	}

	// Verify the tag exists in the bare repo.
	fsPath, ok := h.resolveFsPath(w, repo)
	if !ok {
		return
	}
	sha, err := h.git.ResolveCommit(fsPath, "refs/tags/"+req.TagName)
	if err != nil {
		if errors.Is(err, gitdomain.ErrRefNotFound) {
			httpx.WriteError(w, http.StatusBadRequest, "tag not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sha == "" {
		httpx.WriteError(w, http.StatusBadRequest, "tag not found")
		return
	}

	rel, err := h.store.Create(r.Context(), repo.ID, req.TagName, sha, req.Title, req.Notes)
	if err != nil {
		if errors.Is(err, releasedomain.ErrReleaseConflict) {
			httpx.WriteError(w, http.StatusConflict, "a release for this tag already exists")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toPublicRelease(repo, rel, nil))
}

// ---- Get ----

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rel, ok := h.loadRelease(w, r, repo, id)
	if !ok {
		return
	}
	assets, err := h.assets.ListByRelease(r.Context(), rel.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublicRelease(repo, rel, assets))
}

// ---- Update ----

type updateReq struct {
	TagName *string `json:"tag_name,omitempty"`
	Title   *string `json:"title,omitempty"`
	Notes   *string `json:"notes,omitempty"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r)
	if !ok {
		return
	}
	if !h.checkWrite(w, r, repo) {
		return
	}

	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rel, ok := h.loadRelease(w, r, repo, id)
	if !ok {
		return
	}

	var req updateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	tagName := rel.TagName
	targetSHA := rel.TargetCommitSHA
	if req.TagName != nil {
		tn := strings.TrimSpace(*req.TagName)
		if tn == "" {
			httpx.WriteError(w, http.StatusBadRequest, "tag_name must not be empty")
			return
		}
		if !isSafeRefName(tn) {
			httpx.WriteError(w, http.StatusBadRequest, "invalid tag_name")
			return
		}
		if !rel.IsDraft && tn != rel.TagName {
			httpx.WriteError(w, http.StatusBadRequest, "cannot change tag_name of a published release")
			return
		}
		fsPath, ok := h.resolveFsPath(w, repo)
		if !ok {
			return
		}
		sha, err := h.git.ResolveCommit(fsPath, "refs/tags/"+tn)
		if err != nil {
			if errors.Is(err, gitdomain.ErrRefNotFound) {
				httpx.WriteError(w, http.StatusBadRequest, "tag not found")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if sha == "" {
			httpx.WriteError(w, http.StatusBadRequest, "tag not found")
			return
		}
		tagName = tn
		targetSHA = sha
	}

	title := rel.Title
	if req.Title != nil {
		title = strings.TrimSpace(*req.Title)
		if title == "" {
			title = tagName
		}
	}
	notes := rel.Notes
	if req.Notes != nil {
		notes = *req.Notes
	}

	updated, err := h.store.Update(r.Context(), id, tagName, targetSHA, title, notes)
	if err != nil {
		if errors.Is(err, releasedomain.ErrReleaseConflict) {
			httpx.WriteError(w, http.StatusConflict, "a release for this tag already exists")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	assets, _ := h.assets.ListByRelease(r.Context(), id)
	httpx.WriteJSON(w, http.StatusOK, toPublicRelease(repo, updated, assets))
}

// ---- Delete ----

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r)
	if !ok {
		return
	}
	if !h.checkWrite(w, r, repo) {
		return
	}

	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	_, ok = h.loadRelease(w, r, repo, id)
	if !ok {
		return
	}

	assets, err := h.assets.ListByRelease(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, a := range assets {
		_ = h.storage.Remove(a.StorageKey)
	}

	if err := h.store.Delete(r.Context(), id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Publish ----

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r)
	if !ok {
		return
	}
	if !h.checkWrite(w, r, repo) {
		return
	}

	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	_, ok = h.loadRelease(w, r, repo, id)
	if !ok {
		return
	}

	published, err := h.store.Publish(r.Context(), id)
	if err != nil {
		if errors.Is(err, releasedomain.ErrReleaseNotDraft) {
			httpx.WriteError(w, http.StatusBadRequest, "release is already published")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	assets, _ := h.assets.ListByRelease(r.Context(), id)
	httpx.WriteJSON(w, http.StatusOK, toPublicRelease(repo, published, assets))
}

// ---- Upload asset ----

func (h *Handler) uploadAsset(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r)
	if !ok {
		return
	}
	if !h.checkWrite(w, r, repo) {
		return
	}

	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	rel, ok := h.loadRelease(w, r, repo, id)
	if !ok {
		return
	}

	if err := r.ParseMultipartForm(100 << 20); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid multipart body")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		httpx.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !isSafeAssetName(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid asset name")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	storageKey := fmt.Sprintf("%d/%s", rel.ID, name)
	sizeBytes, err := h.storage.Store(storageKey, file)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "store asset: "+err.Error())
		return
	}

	_, err = h.assets.Create(r.Context(), rel.ID, name, contentType, sizeBytes, storageKey)
	if err != nil {
		if errors.Is(err, releasedomain.ErrAssetConflict) {
			_ = h.storage.Remove(storageKey)
			httpx.WriteError(w, http.StatusConflict, "an asset with this name already exists")
			return
		}
		_ = h.storage.Remove(storageKey)
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return the full release so the frontend can replace its state.
	assets, _ := h.assets.ListByRelease(r.Context(), rel.ID)
	httpx.WriteJSON(w, http.StatusCreated, toPublicRelease(repo, rel, assets))
}

// ---- Delete asset ----

func (h *Handler) deleteAsset(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r)
	if !ok {
		return
	}
	if !h.checkWrite(w, r, repo) {
		return
	}

	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	assetID, ok := parseID(w, r, "assetID")
	if !ok {
		return
	}

	rel, ok := h.loadRelease(w, r, repo, id)
	if !ok {
		return
	}

	asset, err := h.assets.GetByID(r.Context(), assetID)
	if err != nil {
		if errors.Is(err, releasedomain.ErrAssetNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "asset not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if asset.ReleaseID != rel.ID {
		httpx.WriteError(w, http.StatusNotFound, "asset not found")
		return
	}

	_ = h.storage.Remove(asset.StorageKey)
	if err := h.assets.Delete(r.Context(), assetID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return the full release so the frontend can replace its state.
	assets, _ := h.assets.ListByRelease(r.Context(), rel.ID)
	httpx.WriteJSON(w, http.StatusOK, toPublicRelease(repo, rel, assets))
}

// ---- Download asset ----

func (h *Handler) downloadAsset(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.loadRepo(w, r)
	if !ok {
		return
	}

	id, ok := parseID(w, r, "id")
	if !ok {
		return
	}
	assetID, ok := parseID(w, r, "assetID")
	if !ok {
		return
	}

	rel, ok := h.loadRelease(w, r, repo, id)
	if !ok {
		return
	}

	asset, err := h.assets.GetByID(r.Context(), assetID)
	if err != nil {
		if errors.Is(err, releasedomain.ErrAssetNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "asset not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if asset.ReleaseID != rel.ID {
		httpx.WriteError(w, http.StatusNotFound, "asset not found")
		return
	}

	reader, err := h.storage.Open(asset.StorageKey)
	if err != nil {
		if errors.Is(err, releasedomain.ErrAssetNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "asset file not found on disk")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", asset.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, asset.Name))
	w.Header().Set("Content-Length", strconv.FormatInt(asset.SizeBytes, 10))
	io.Copy(w, reader)
}

// ---- Helpers ----

// bearerTokenFromHeader extracts the Bearer token from the Authorization header.
func bearerTokenFromHeader(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errors.New("missing authorization header")
	}
	const pfx = "Bearer "
	if !strings.HasPrefix(h, pfx) {
		return "", errors.New("authorization must be Bearer")
	}
	tok := strings.TrimSpace(h[len(pfx):])
	if tok == "" {
		return "", errors.New("empty bearer token")
	}
	return tok, nil
}

var safeRefRe = regexp.MustCompile(`^[A-Za-z0-9._\-/]{1,200}$`)

func isSafeRefName(name string) bool {
	if len(name) > 200 || strings.HasPrefix(name, "-") {
		return false
	}
	return safeRefRe.MatchString(name)
}

var basicNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]{0,99}$`)

func isSafeAssetName(name string) bool {
	if len(name) > 200 || strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	return basicNameRe.MatchString(name)
}

func (h *Handler) loadRepo(w http.ResponseWriter, r *http.Request) (*repodomain.Repo, bool) {
	ownerName := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "name")
	if !basicNameRe.MatchString(ownerName) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid owner")
		return nil, false
	}
	if !basicNameRe.MatchString(repoName) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return nil, false
	}

	owner, err := h.orgResolver.ResolveOwner(r.Context(), ownerName)
	if err != nil {
		if errors.Is(err, orgdomain.ErrOwnerNotFound) || errors.Is(err, orgdomain.ErrOrgReserved) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	repo, err := h.repos.GetByOwnerAndName(r.Context(), repodomain.OwnerKind(owner.Kind), owner.ID, repoName)
	if err != nil {
		if errors.Is(err, repodomain.ErrRepoNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return repo, true
}

func (h *Handler) loadRelease(w http.ResponseWriter, r *http.Request, repo *repodomain.Repo, id int64) (*releasedomain.Release, bool) {
	rel, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, releasedomain.ErrReleaseNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "release not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if rel.RepoID != repo.ID {
		httpx.WriteError(w, http.StatusNotFound, "release not found")
		return nil, false
	}
	return rel, true
}

func (h *Handler) checkWrite(w http.ResponseWriter, r *http.Request, repo *repodomain.Repo) bool {
	// First, try workflow token auth (Bearer hangrix_wf_...).
	if h.wfTokenValidator != nil {
		tok, err := bearerTokenFromHeader(r)
		if err == nil && strings.HasPrefix(tok, "hangrix_wf_") {
			tokenRepoID, err := h.wfTokenValidator.ValidateWorkflowToken(r.Context(), tok)
			if err == nil && tokenRepoID == repo.ID {
				return true
			}
		}
	}

	// Fall back to user (cookie) auth.
	caller, _ := authdomain.UserFromRequest(r)
	can, err := canWriteRepo(r.Context(), h.orgResolver, caller, repo)
	if err != nil || !can {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return false
	}
	return true
}

func (h *Handler) resolveFsPath(w http.ResponseWriter, repo *repodomain.Repo) (string, bool) {
	path, err := h.resolver.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "resolve path: "+err.Error())
		return "", false
	}
	return path, true
}

func parseID(w http.ResponseWriter, r *http.Request, param string) (int64, bool) {
	raw := chi.URLParam(r, param)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid "+param)
		return 0, false
	}
	return id, true
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

// canWriteRepo mirrors the repo handler's canWriteContents: caller may
// write if they are the owner, an admin, or (for user-owned repos) a write
// member. For org-owned repos, only org owners can write.
func canWriteRepo(ctx context.Context, orgResolver orgdomain.Resolver, caller *userdomain.User, repo *repodomain.Repo) (bool, error) {
	if caller == nil {
		return false, nil
	}
	if caller.Role == userdomain.RoleAdmin {
		return true, nil
	}
	switch repo.OwnerKind {
	case repodomain.OwnerKindUser:
		if caller.ID == repo.OwnerID {
			return true, nil
		}
		return false, nil
	case repodomain.OwnerKindOrg:
		role, ok, err := orgResolver.Membership(ctx, repo.OwnerID, caller.ID)
		if err != nil || !ok {
			return false, err
		}
		return role == orgdomain.RoleOwner, nil
	}
	return false, nil
}
