// Package handler exposes the issue module's HTTP surface. The endpoints are
// all scoped to /api/repos/{owner}/{name}/issues so they slot in next to the
// repo module's existing routes. Authorization mirrors repo's:
//
//   - Reading issues / comments — anyone who can read the repo.
//   - Creating issues / comments — any authenticated user with read access.
//   - Mutating state, title/body, merging — owner or admin only.
//
// The handler also owns the "open issue branch" lifecycle: it reads/writes
// HeadSHA, records commit_pushed events post-receive, and toggles the
// receive-pack hook sidecar so the git CLI enforces the same branch-bound
// rules as the web API.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	repoinfra "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

type Handler struct {
	issues     domain.Store
	repos      repodomain.Store
	storage    *repoinfra.Storage
	git        gitdomain.Git
	users      userdomain.Repo
	resolver   orgdomain.Resolver
	middleware authdomain.Middleware
}

type HandlerDeps struct {
	Issues     domain.Store
	Repos      repodomain.Store
	Storage    *repoinfra.Storage
	Git        gitdomain.Git
	Users      userdomain.Repo
	Resolver   orgdomain.Resolver
	Middleware authdomain.Middleware
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		issues:     deps.Issues,
		repos:      deps.Repos,
		storage:    deps.Storage,
		git:        deps.Git,
		users:      deps.Users,
		resolver:   deps.Resolver,
		middleware: deps.Middleware,
	}
}

var (
	usernameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)
	repoNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)
)

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/repos/{owner}/{name}/issues", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Get("/{number}", h.get)
		r.Patch("/{number}", h.patch)
		r.Get("/{number}/timeline", h.timeline)
		r.Get("/{number}/diff", h.diff)
		r.Get("/{number}/commits", h.commits)
		r.Get("/{number}/children", h.children)
		r.Post("/{number}/comments", h.createComment)
		r.Post("/{number}/merge", h.merge)
		r.Post("/{number}/sync", h.sync)
	})
}

// --- DTOs ---

type publicIssue struct {
	ID             int64      `json:"id"`
	RepoID         int64      `json:"repo_id"`
	Number         int64      `json:"number"`
	AuthorID       int64      `json:"author_id"`
	AuthorUsername string     `json:"author_username"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	State          string     `json:"state"`
	BranchName     string     `json:"branch_name"`
	BaseBranch     string     `json:"base_branch"`
	HeadSHA        string     `json:"head_sha"`
	ParentNumber   int64      `json:"parent_number"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	MergedAt       *time.Time `json:"merged_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func toPublic(i *domain.Issue) publicIssue {
	return publicIssue{
		ID:             i.ID,
		RepoID:         i.RepoID,
		Number:         i.Number,
		AuthorID:       i.AuthorID,
		AuthorUsername: i.AuthorName,
		Title:          i.Title,
		Body:           i.Body,
		State:          string(i.State),
		BranchName:     i.BranchName,
		BaseBranch:     i.BaseBranch,
		HeadSHA:        i.HeadSHA,
		ParentNumber:   i.ParentNumber,
		MergeCommitSHA: i.MergeCommitSHA,
		MergedAt:       i.MergedAt,
		CreatedAt:      i.CreatedAt,
		UpdatedAt:      i.UpdatedAt,
	}
}

type publicComment struct {
	ID             int64     `json:"id"`
	IssueID        int64     `json:"issue_id"`
	AuthorID       int64     `json:"author_id"`
	AuthorUsername string    `json:"author_username"`
	Body           string    `json:"body"`
	FilePath       string    `json:"file_path"`
	Line           int       `json:"line"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func toPublicComment(c *domain.Comment) publicComment {
	return publicComment{
		ID:             c.ID,
		IssueID:        c.IssueID,
		AuthorID:       c.AuthorID,
		AuthorUsername: c.AuthorName,
		Body:           c.Body,
		FilePath:       c.FilePath,
		Line:           c.Line,
		CreatedAt:      c.CreatedAt,
		UpdatedAt:      c.UpdatedAt,
	}
}

type publicEvent struct {
	ID            int64           `json:"id"`
	IssueID       int64           `json:"issue_id"`
	Kind          string          `json:"kind"`
	Payload       json.RawMessage `json:"payload"`
	ActorID       int64           `json:"actor_id"`
	ActorUsername string          `json:"actor_username"`
	CreatedAt     time.Time       `json:"created_at"`
}

func toPublicEvent(e *domain.Event) publicEvent {
	pl := json.RawMessage(e.Payload)
	if len(pl) == 0 {
		pl = json.RawMessage(`{}`)
	}
	return publicEvent{
		ID:            e.ID,
		IssueID:       e.IssueID,
		Kind:          string(e.Kind),
		Payload:       pl,
		ActorID:       e.ActorID,
		ActorUsername: e.ActorName,
		CreatedAt:     e.CreatedAt,
	}
}

// --- helpers ---

type repoCtx struct {
	repo   *repodomain.Repo
	fsPath string
}

func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request) (*repoCtx, bool) {
	owner := chi.URLParam(r, "owner")
	name := chi.URLParam(r, "name")
	if !usernameRe.MatchString(owner) || !repoNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid repo")
		return nil, false
	}
	resolved, err := h.resolver.ResolveOwner(r.Context(), owner)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "repo not found")
		return nil, false
	}
	repo, err := h.repos.GetByOwnerAndName(r.Context(), repodomain.OwnerKind(resolved.Kind), resolved.ID, name)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "repo not found")
		return nil, false
	}
	caller, _ := authdomain.UserFromRequest(r)
	if repo.Visibility == repodomain.VisibilityPrivate {
		ok, err := h.canRead(r, caller, repo)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return nil, false
		}
		if !ok {
			httpx.WriteError(w, http.StatusForbidden, "forbidden")
			return nil, false
		}
	}
	fsPath, err := h.storage.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "resolve path: "+err.Error())
		return nil, false
	}
	return &repoCtx{repo: repo, fsPath: fsPath}, true
}

// canRead mirrors the repo handler's canReadRepo: user-owned → user is
// owner, org-owned → user is any-role member, admin always.
func (h *Handler) canRead(r *http.Request, caller *userdomain.User, repo *repodomain.Repo) (bool, error) {
	if caller == nil {
		return false, nil
	}
	if caller.Role == userdomain.RoleAdmin {
		return true, nil
	}
	switch repo.OwnerKind {
	case repodomain.OwnerKindUser:
		return caller.ID == repo.OwnerID, nil
	case repodomain.OwnerKindOrg:
		_, ok, err := h.resolver.Membership(r.Context(), repo.OwnerID, caller.ID)
		return ok, err
	}
	return false, nil
}

// canManage gates owner-only issue writes (merge, transition closed/merged).
// Mirrors the repo handler's canWriteRepo: org-owned repos require owner
// role inside the org; admin always.
func (h *Handler) canManage(r *http.Request, caller *userdomain.User, repo *repodomain.Repo) bool {
	if caller == nil {
		return false
	}
	if caller.Role == userdomain.RoleAdmin {
		return true
	}
	switch repo.OwnerKind {
	case repodomain.OwnerKindUser:
		return caller.ID == repo.OwnerID
	case repodomain.OwnerKindOrg:
		role, ok, err := h.resolver.Membership(r.Context(), repo.OwnerID, caller.ID)
		if err != nil || !ok {
			return false
		}
		return role == orgdomain.RoleOwner
	}
	return false
}

func (h *Handler) loadIssue(w http.ResponseWriter, r *http.Request, repoID int64) (*domain.Issue, bool) {
	raw := chi.URLParam(r, "number")
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid issue number")
		return nil, false
	}
	iss, err := h.issues.GetByNumber(r.Context(), repoID, n)
	if err != nil {
		if errors.Is(err, domain.ErrIssueNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "issue not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return iss, true
}

// --- handlers ---

type createReq struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// ParentNumber, when non-zero, links the new issue as a child of the
	// referenced issue. The child's base branch is automatically pointed at
	// the parent's issue branch so merging a child fast-forwards into the
	// parent's working line. Top-level issues use the repo default branch
	// as their base — the client never picks the branch explicitly (M4
	// design: base is implicit context, not a user choice).
	ParentNumber int64 `json:"parent_number,omitempty"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" || len(title) > 200 {
		httpx.WriteError(w, http.StatusBadRequest, "title is required (1-200 chars)")
		return
	}

	base := rc.repo.DefaultBranch
	var parentID, parentNumber int64
	if req.ParentNumber > 0 {
		parent, err := h.issues.GetByNumber(r.Context(), rc.repo.ID, req.ParentNumber)
		if err != nil {
			if errors.Is(err, domain.ErrIssueNotFound) {
				httpx.WriteError(w, http.StatusBadRequest, "parent issue not found")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if parent.State != domain.StateOpen {
			httpx.WriteError(w, http.StatusConflict, "parent issue is not open")
			return
		}
		base = parent.BranchName
		parentID = parent.ID
		parentNumber = parent.Number
	}

	caller, _ := authdomain.UserFromRequest(r)
	iss, err := h.issues.Create(r.Context(), rc.repo.ID, caller.ID, title, req.Body, base, parentID, parentNumber)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Keep the receive-pack sidecar in sync so a fresh issue is immediately
	// pushable.
	h.refreshIssueMode(r, rc)
	httpx.WriteJSON(w, http.StatusCreated, toPublic(iss))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	state := domain.State(strings.TrimSpace(r.URL.Query().Get("state")))
	if state != "" && !state.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "invalid state")
		return
	}
	offset := parseInt32(r.URL.Query().Get("offset"), 0)
	limit := parseInt32(r.URL.Query().Get("limit"), 50)

	list, total, err := h.issues.List(r.Context(), rc.repo.ID, domain.ListFilter{
		State:  state,
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicIssue, 0, len(list))
	for _, i := range list {
		items = append(items, toPublic(i))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublic(iss))
}

type patchReq struct {
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
	State *string `json:"state,omitempty"`
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	caller, _ := authdomain.UserFromRequest(r)
	authorOrManager := caller.ID == iss.AuthorID || h.canManage(r, caller, rc.repo)
	if !authorOrManager {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	var req patchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	updated := iss
	if req.Title != nil || req.Body != nil {
		title := iss.Title
		if req.Title != nil {
			title = strings.TrimSpace(*req.Title)
			if title == "" || len(title) > 200 {
				httpx.WriteError(w, http.StatusBadRequest, "title invalid")
				return
			}
		}
		body := iss.Body
		if req.Body != nil {
			body = *req.Body
		}
		titleChanged := title != iss.Title
		var err error
		updated, err = h.issues.UpdateTitleBody(r.Context(), iss.ID, title, body)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if titleChanged {
			payload, _ := json.Marshal(domain.TitleChangedPayload{From: iss.Title, To: title})
			_, _ = h.issues.CreateEvent(r.Context(), iss.ID, domain.EventTitleChanged, payload, caller.ID)
		}
	}

	if req.State != nil {
		want := domain.State(*req.State)
		if !want.Valid() {
			httpx.WriteError(w, http.StatusBadRequest, "invalid state")
			return
		}
		// Closed ↔ open only — merged is set exclusively via the merge endpoint.
		if want == domain.StateMerged {
			httpx.WriteError(w, http.StatusBadRequest, "merge through POST /merge to enter merged state")
			return
		}
		if want != updated.State {
			// Re-opening a merged issue is not supported.
			if updated.State == domain.StateMerged {
				httpx.WriteError(w, http.StatusConflict, "merged issues cannot change state")
				return
			}
			next, err := h.issues.UpdateState(r.Context(), iss.ID, want, "")
			if err != nil {
				httpx.WriteError(w, http.StatusInternalServerError, err.Error())
				return
			}
			payload, _ := json.Marshal(domain.StateChangedPayload{From: updated.State, To: want})
			_, _ = h.issues.CreateEvent(r.Context(), iss.ID, domain.EventStateChanged, payload, caller.ID)
			updated = next
			h.refreshIssueMode(r, rc)
		}
	}

	httpx.WriteJSON(w, http.StatusOK, toPublic(updated))
}

// timeline returns the merged comment + event stream sorted by created_at.
// The handler does the merge sort so the client never needs to know about
// two separate tables.
func (h *Handler) timeline(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	comments, err := h.issues.ListComments(r.Context(), iss.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	events, err := h.issues.ListEvents(r.Context(), iss.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"comments": collectComments(comments),
		"events":   collectEvents(events),
	})
}

func collectComments(in []*domain.Comment) []publicComment {
	out := make([]publicComment, 0, len(in))
	for _, c := range in {
		out = append(out, toPublicComment(c))
	}
	return out
}

func collectEvents(in []*domain.Event) []publicEvent {
	out := make([]publicEvent, 0, len(in))
	for _, e := range in {
		out = append(out, toPublicEvent(e))
	}
	return out
}

// children returns every sub-issue whose parent_id points at this issue.
// Used by the detail page to render the "sub-issues" rail without doing a
// per-row lookup.
func (h *Handler) children(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	kids, err := h.issues.ListChildren(r.Context(), iss.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]publicIssue, 0, len(kids))
	for _, k := range kids {
		out = append(out, toPublic(k))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// commits returns the commit list "base..issue_branch" — the commits the
// issue introduces relative to its base. Walks ListCommits from the head and
// stops at the first commit that's already an ancestor of base. Capped at
// 200 entries: anything larger than that is past the point where a list
// view is useful anyway, and the cap keeps the IsAncestor probes bounded.
//
// Empty head (no commits pushed yet) yields []; bad-ref / missing-branch on
// disk also yields [] rather than 500 because the UI should render a clean
// "nothing here yet" state in both cases.
func (h *Handler) commits(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	if iss.HeadSHA == "" {
		httpx.WriteJSON(w, http.StatusOK, []*gitdomain.Commit{})
		return
	}
	const cap = 200
	all, err := h.git.ListCommits(rc.fsPath, iss.BranchName, 0, cap)
	if err != nil {
		if errors.Is(err, gitdomain.ErrEmptyRepo) || errors.Is(err, gitdomain.ErrRefNotFound) {
			httpx.WriteJSON(w, http.StatusOK, []*gitdomain.Commit{})
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]*gitdomain.Commit, 0, len(all))
	for _, c := range all {
		isAncestor, err := h.git.IsAncestor(rc.fsPath, c.SHA, iss.BaseBranch)
		if err == nil && isAncestor {
			break
		}
		out = append(out, c)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// diff returns the diff "base..issue_branch". When the issue branch has not
// been pushed yet we return an empty list so the UI can show a clean state.
func (h *Handler) diff(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	if iss.HeadSHA == "" {
		httpx.WriteJSON(w, http.StatusOK, []*gitdomain.FileDiff{})
		return
	}
	diffs, err := h.git.DiffRefs(rc.fsPath, iss.BaseBranch, iss.BranchName)
	if err != nil {
		if errors.Is(err, gitdomain.ErrRefNotFound) {
			httpx.WriteJSON(w, http.StatusOK, []*gitdomain.FileDiff{})
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, diffs)
}

type createCommentReq struct {
	Body     string `json:"body"`
	FilePath string `json:"file_path,omitempty"`
	Line     int    `json:"line,omitempty"`
}

func (h *Handler) createComment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	var req createCommentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		httpx.WriteError(w, http.StatusBadRequest, "body is required")
		return
	}
	if req.Line < 0 {
		req.Line = 0
	}
	caller, _ := authdomain.UserFromRequest(r)
	c, err := h.issues.CreateComment(r.Context(), iss.ID, caller.ID, body, strings.TrimSpace(req.FilePath), req.Line)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toPublicComment(c))
}

type mergeReq struct {
	Message string `json:"message,omitempty"`
}

// merge runs MergeBranch on the bare repo. Only owner or admin may merge.
// On success the issue transitions to State=merged and the timeline gets a
// branch_merged event. The issue branch is **kept** post-merge so the diff
// stays inspectable; deleting it would also require us to relax the guard.
func (h *Handler) merge(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	caller, _ := authdomain.UserFromRequest(r)
	if !h.canManage(r, caller, rc.repo) {
		httpx.WriteError(w, http.StatusForbidden, "only the repo owner can merge")
		return
	}
	if iss.State != domain.StateOpen {
		httpx.WriteError(w, http.StatusConflict, "issue is not open")
		return
	}
	headSHA, err := h.git.ResolveCommit(rc.fsPath, iss.BranchName)
	if err != nil || headSHA == "" {
		httpx.WriteError(w, http.StatusConflict, "issue branch has no commits yet")
		return
	}

	var req mergeReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		msg = fmt.Sprintf("Merge issue #%d: %s", iss.Number, iss.Title)
	}

	mergeSHA, mode, err := h.git.MergeBranch(rc.fsPath, iss.BaseBranch, iss.BranchName, msg, gitdomain.Signature{
		Name:  caller.Username,
		Email: caller.Email,
		When:  time.Now(),
	})
	if err != nil {
		if errors.Is(err, gitdomain.ErrMergeConflict) {
			httpx.WriteError(w, http.StatusConflict, "merge conflict — rebase the issue branch onto "+iss.BaseBranch)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	updated, err := h.issues.UpdateState(r.Context(), iss.ID, domain.StateMerged, mergeSHA)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	mergedAt := time.Now()
	updated.MergedAt = &mergedAt

	mergePayload, _ := json.Marshal(domain.BranchMergedPayload{
		IntoBranch: iss.BaseBranch,
		FromBranch: iss.BranchName,
		MergeSHA:   mergeSHA,
		Mode:       mode,
	})
	_, _ = h.issues.CreateEvent(r.Context(), iss.ID, domain.EventBranchMerged, mergePayload, caller.ID)
	statePayload, _ := json.Marshal(domain.StateChangedPayload{From: domain.StateOpen, To: domain.StateMerged})
	_, _ = h.issues.CreateEvent(r.Context(), iss.ID, domain.EventStateChanged, statePayload, caller.ID)

	// Closing the issue removes it from the "open" list; re-sync the
	// receive-pack sidecar so a follow-up push to issue/<n> is rejected.
	h.refreshIssueMode(r, rc)

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"issue":     toPublic(updated),
		"merge_sha": mergeSHA,
		"mode":      mode,
	})
}

// sync inspects the on-disk issue branch and updates HeadSHA + commit_pushed
// events to reflect any commits the user pushed since the last sync. Called
// explicitly from the receive-pack hook chain and also exposed as an API for
// the web UI to nudge after a push from the CLI. Idempotent.
func (h *Handler) sync(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	if err := h.SyncIssueBranch(r.Context(), rc.repo, rc.fsPath, iss, 0); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	refreshed, err := h.issues.GetByNumber(r.Context(), rc.repo.ID, iss.Number)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublic(refreshed))
}

// --- utilities ---

func parseInt32(raw string, def int32) int32 {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return int32(n)
}
