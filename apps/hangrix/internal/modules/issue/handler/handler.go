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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/kv"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	issueservice "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/service"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	repoinfra "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

type Handler struct {
	issues      domain.Store
	patches     domain.PatchStore
	repos       repodomain.Store
	storage     *repoinfra.Storage
	git         gitdomain.Git
		cache       *kv.RepoCache
	users       userdomain.Repo
	resolver    orgdomain.Resolver
	middleware  authdomain.Middleware
	protections repodomain.ProtectionStore
	// agent_session lifecycle hooks. All four are optional (nil-safe
	// call sites) so the handler keeps working in test configurations
	// where the module isn't loaded; in production ioc binds all of
	// them, so the nil branches never fire.
	spawner    agentsessiondomain.Spawner
	archiver   agentsessiondomain.Archiver
	auditor    agentsessiondomain.Auditor
	controller agentsessiondomain.Controller
	attachments *issueservice.AttachmentService
	// guards are BranchWriteGuard implementations. When nil (tests
	// without the repo module) the handler skips guard checks.
	guards []repodomain.BranchWriteGuard
}

type HandlerDeps struct {
	Issues      domain.Store
	Patches     domain.PatchStore
	Repos       repodomain.Store
	Storage     *repoinfra.Storage
	Git         gitdomain.Git
	Users       userdomain.Repo
	Resolver    orgdomain.Resolver
	Middleware  authdomain.Middleware
	// Protections is the branch_protections store from the repo module.
	// Used by merge to honour forbid_delete rules before deleting the
	// issue branch post-merge. Nil-safe — the handler skips protection
	// checks when absent (tests).
	Protections repodomain.ProtectionStore
		// Cache provides Redis-backed invalidation for git-read caches.
		// When nil (tests, no Redis) the handler silently skips flushes.
		Cache *kv.RepoCache
	// Spawner + Archiver + Auditor + Controller come from the
	// agent_session module. Wired through ioc.
	Spawner    agentsessiondomain.Spawner
	Archiver   agentsessiondomain.Archiver
	Auditor    agentsessiondomain.Auditor
	Controller agentsessiondomain.Controller
	// Attachments is the attachment service (validation, hashing, storage).
	Attachments *issueservice.AttachmentService
	// Guards are BranchWriteGuard implementations. When nil (tests
	// without the repo module) the handler skips guard checks.
	Guards []repodomain.BranchWriteGuard
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		issues:     deps.Issues,
		patches:    deps.Patches,
		repos:      deps.Repos,
		storage:    deps.Storage,
		git:        deps.Git,
			cache:      deps.Cache,
		users:      deps.Users,
		resolver:   deps.Resolver,
		middleware: deps.Middleware,
		spawner:    deps.Spawner,
		archiver:   deps.Archiver,
		auditor:    deps.Auditor,
		controller: deps.Controller,
		attachments: deps.Attachments,
		guards:      deps.Guards,
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
		// M7c — agent session inspector. Same visibility rules as the
		// rest of the issue API (resolveRepo gates on the repo's
		// public/private + caller membership).
		r.Get("/{number}/agent-sessions", h.listAgentSessions)
		r.Get("/{number}/agent-sessions/{sid}/messages", h.listAgentSessionMessages)
		// Per-session controls. Stop/resume need the issue's manage
		// permission so any repo reader can't kill another user's
		// running agent; the existing canManage gate is the same one
		// Attachments (upload, list, download, delete).
		r.Post("/{number}/attachments", h.createAttachment)
		r.Get("/{number}/attachments", h.listAttachments)
		r.Get("/{number}/attachments/{id}", h.getAttachment)
		r.Delete("/{number}/attachments/{id}", h.deleteAttachment)

		// merge uses.
		r.Post("/{number}/agent-sessions/{sid}/stop", h.stopAgentSession)
		r.Post("/{number}/agent-sessions/{sid}/resume", h.resumeAgentSession)
		r.Delete("/{number}/agent-sessions/{sid}", h.deleteAgentSession)

		// Patch submissions (patch-first contribution model, issue #102).
		r.Get("/{number}/patches", h.listPatches)
		r.Get("/{number}/patches/{patchID}", h.getPatch)
		r.Post("/{number}/patches/{patchID}/apply", h.applyPatch)
		r.Post("/{number}/patches/{patchID}/reject", h.rejectPatch)
		r.Post("/{number}/patches/{patchID}/void", h.voidPatch)
	})
	// Mention-suggestion list: the comment editor reads this once per
	// issue page load to populate the `@` autocomplete dropdown with
	// every agent role declared in the repo's host yaml. Returning the
	// full list (rather than a query-filtered prefix endpoint) keeps the
	// dropdown filterable client-side without a roundtrip per keystroke.
	r.With(h.middleware.RequireAuth).
		Get("/api/repos/{owner}/{name}/mention-suggestions", h.mentionSuggestions)
}

// --- DTOs ---

type publicIssue struct {
	ID             int64      `json:"id"`
	RepoID         int64      `json:"repo_id"`
	Number         int64      `json:"number"`
	AuthorID       int64      `json:"author_id"`
	AuthorUsername string     `json:"author_username"`
	// AgentRole is set on agent-created issues; empty for human-created.
	AgentRole      string     `json:"agent_role,omitempty"`
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
	// ReviewStatus is populated on issue detail; nil on list responses.
	ReviewStatus *domain.ReviewStatus `json:"review_status,omitempty"`
}

func toPublic(i *domain.Issue) publicIssue {
	return publicIssue{
		ID:             i.ID,
		RepoID:         i.RepoID,
		Number:         i.Number,
		AuthorID:       i.AuthorID,
		AuthorUsername: i.AuthorName,
		AgentRole:      i.AgentRole,
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
	// AgentRole is set on agent-authored comments. Empty for human
	// comments. The frontend uses it to render a role chip / avatar.
	AgentRole string    `json:"agent_role,omitempty"`
	Body      string    `json:"body"`
	FilePath  string    `json:"file_path"`
	Line      int       `json:"line"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toPublicComment(c *domain.Comment) publicComment {
	return publicComment{
		ID:             c.ID,
		IssueID:        c.IssueID,
		AuthorID:       c.AuthorID,
		AuthorUsername: c.AuthorName,
		AgentRole:      c.AgentRole,
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
	// AgentRole is set on agent-authored events (review_vote, agent
	// merges, etc.). Empty for human / system events.
	AgentRole string    `json:"agent_role,omitempty"`
	CreatedAt time.Time `json:"created_at"`
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
		AgentRole:     e.AgentRole,
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

		// Verify the base ref is resolvable before writing anything to the
		// database — empty repos (no commits) and unresolvable base refs must
		// fail early so we never leave a DB record with no matching git ref.
		baseSHA, err := h.git.ResolveCommit(rc.fsPath, base)
		if err != nil {
			if errors.Is(err, gitdomain.ErrRefNotFound) {
				httpx.WriteError(w, http.StatusBadRequest, "base ref not found: "+base)
			} else {
				httpx.WriteError(w, http.StatusInternalServerError, "resolve base ref: "+err.Error())
			}
			return
		}
		if baseSHA == "" {
			httpx.WriteError(w, http.StatusBadRequest, "base branch has no commits yet: "+base)
			return
		}

	caller, _ := authdomain.UserFromRequest(r)
	iss, err := h.issues.Create(r.Context(), rc.repo.ID, caller.ID, title, req.Body, base, "", parentID, parentNumber)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Create the branch ref in the bare repo so it appears in branch
	// listings immediately — before anyone pushes. The new branch points
	// at the base branch's tip (pre-validated above), so a subsequent
	// push is a normal fast-forward. If branch creation fails here, it's
	// a real error — the caller already validated the base ref — and we
	// must not return 201 for an issue that has no matching git ref.
	if err := h.git.CreateBranch(rc.fsPath, iss.BranchName, base); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "create branch ref: "+err.Error())
		return
	}
	// Invalidate the /refs cache for this repo so the new branch is
	// visible immediately — no waiting for the 15s TTL to expire.
	if h.cache != nil {
		h.cache.InvalidateRepo(r.Context(), rc.repo.ID)
	}
	// Fire issue.opened at the agent_session spawner so any role whose
	// triggers include issue.opened wakes on its own. Failures don't
	// block issue creation — operator repairs the host yaml then nudges
	// the issue.
	h.fireIssueOpened(r.Context(), rc.repo.ID, iss.Number, caller.ID)
	httpx.WriteJSON(w, http.StatusCreated, toPublic(iss))
}

// fireIssueOpened dispatches the spawn event. Nil-safe so test
// configurations without the agent_session module still work; production
// ioc binding always populates spawner.
//
// Whole-config spawn errors (typically a malformed `.hangrix/agents.yml`
// at the default-branch tip — e.g. an agent rewrote the file on an
// issue branch and the merged version no longer parses) used to be
// dropped silently here, which surfaced as "I opened an issue but no
// agent woke up" with zero feedback. Log them so an operator can
// correlate against the issue create. Per-role failures are already
// logged inside the spawner.
func (h *Handler) fireIssueOpened(ctx context.Context, repoID, issueNumber, actorID int64) {
	if h.spawner == nil {
		return
	}
	if _, err := h.spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
		Trigger:     agentsconfig.TriggerIssueOpened,
		CauseKind:   agentsessiondomain.CauseKindIssueOpened,
		CauseID:     "",
		RepoID:      repoID,
		IssueNumber: int32(issueNumber),
		ActorID:     actorID,
	}); err != nil {
		log.Printf("issue: fireIssueOpened repo=%d issue=%d: %v", repoID, issueNumber, err)
	}
}

// fireIssueClosed flips every live session on the issue to archived.
// Same nil-safe stance as fireIssueOpened.
func (h *Handler) fireIssueClosed(ctx context.Context, repoID int64, issueNumber int32) {
	if h.archiver == nil {
		return
	}
	_, _ = h.archiver.OnIssueClosed(ctx, repoID, issueNumber)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	state := domain.State(strings.TrimSpace(r.URL.Query().Get("state")))
	if state == "all" {
		state = ""
	}
	if state != "" && !state.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "invalid state")
		return
	}
	offset := parseInt32(r.URL.Query().Get("offset"), 0)
	limit := parseInt32(r.URL.Query().Get("limit"), 20)

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
	pub := toPublic(iss)
	// Attach server-computed review status so the frontend doesn't need
	// to derive it from the timeline.
	events, err := h.issues.ListEvents(r.Context(), iss.ID)
	if err == nil {
		pub.ReviewStatus = domain.ComputeReviewStatus(iss, events)
	}
	httpx.WriteJSON(w, http.StatusOK, pub)
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
			// Issue transitioned to closed → archive every live session
			// on it. Transition from closed back to open does NOT
			// resurrect sessions: per spec, archived is terminal and
			// re-opening doesn't roll back the archive (a re-opened
			// issue can spawn fresh sessions if its triggers fire again,
			// but doesn't unarchive the prior ones).
			if want == domain.StateClosed {
				h.fireIssueClosed(r.Context(), rc.repo.ID, int32(iss.Number))
			}
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
//
// Once the issue is merged the base branch has absorbed every commit on the
// issue branch (trivially for fast-forward, via the merge commit's second
// parent otherwise), so the live "ancestor of base" check would short-circuit
// to []. The merge handler captures the pre-merge base tip on the branch_merged
// event payload; we use that as the stop point instead.
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
	stopRef := iss.BaseBranch
	if iss.State == domain.StateMerged {
		if pre := h.preMergeBaseRef(r.Context(), rc.fsPath, iss); pre != "" {
			stopRef = pre
		}
	}
	out := make([]*gitdomain.Commit, 0, len(all))
	for _, c := range all {
		isAncestor, err := h.git.IsAncestor(rc.fsPath, c.SHA, stopRef)
		if err == nil && isAncestor {
			break
		}
		out = append(out, c)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// preMergeBaseRef recovers the base-branch tip as of the moment iss was
// merged. New merges stamp this onto the branch_merged event payload; for
// legacy events we can still reconstruct it for "merge-commit" mode by
// reading the merge commit's first parent. Returns "" if neither path works
// (e.g. legacy fast-forward merge) — callers should fall back to the live
// base branch and accept that the list may collapse to empty.
func (h *Handler) preMergeBaseRef(ctx context.Context, fsPath string, iss *domain.Issue) string {
	events, err := h.issues.ListEvents(ctx, iss.ID)
	if err != nil {
		return ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Kind != domain.EventBranchMerged {
			continue
		}
		var p domain.BranchMergedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return ""
		}
		if p.BaseSHA != "" {
			return p.BaseSHA
		}
		if p.Mode == "merge-commit" && iss.MergeCommitSHA != "" {
			mc, err := h.git.ListCommits(fsPath, iss.MergeCommitSHA, 0, 1)
			if err == nil && len(mc) == 1 && len(mc[0].ParentSHAs) > 0 {
				return mc[0].ParentSHAs[0]
			}
		}
		return ""
	}
	return ""
}

// diff returns the changes introduced by this issue. When the issue branch
// has not been pushed yet we return an empty list so the UI can show a clean
// state.
//
// For open issues we use merge-base diff (DiffMergeBase), equivalent to
// `git diff base...issue_branch`, so unrelated work merged into base after
// the issue branched off doesn't appear in the diff.
//
// Once the issue is merged, base has absorbed the issue branch, so merging
// and merge-base would both show nothing. The merge handler stamps the
// pre-merge base SHA onto the branch_merged event payload; we read it back
// via preMergeBaseRef and diff from there so the diff continues to mean
// "what the issue introduced".
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
	var (
		diffs []*gitdomain.FileDiff
		err   error
	)
	if iss.State == domain.StateMerged {
		if pre := h.preMergeBaseRef(r.Context(), rc.fsPath, iss); pre != "" {
			diffs, err = h.git.DiffRefs(rc.fsPath, pre, iss.BranchName)
		}
	}
	if diffs == nil && err == nil {
		diffs, err = h.git.DiffMergeBase(rc.fsPath, iss.BaseBranch, iss.BranchName)
	}
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

	// Scan the comment body for [attachment:N] / ![attachment:N] tokens
	// and transition matching attachments from uploaded → attached.
	// Best-effort — a missing or already-deleted attachment is not an error.
	if h.attachments != nil {
		re := regexp.MustCompile(`!?\[attachment:(\d+)\]`)
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			if attID, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				_ = h.attachments.MarkAttached(r.Context(), attID, c.ID)
			}
		}
	}
	// Fan the comment out to any subscribing roles. Best-effort — a
	// host-yaml hiccup must not block the comment write itself.
	h.fireCommentTriggers(r, rc, iss, c)
	httpx.WriteJSON(w, http.StatusCreated, toPublicComment(c))
}

// fireCommentTriggers is the comment → agent fan-out. The platform fires
// one `issue.comment` event per comment; each subscribed role's
// TriggerSpec.CommentFilter decides whether to wake (mentioned_only /
// from_roles / from_users). Mention parsing happens once here and the
// list rides on TriggerInput.Comment so the spawner can evaluate
// mentioned_only without re-reading the body.
//
// Any authenticated commenter (read access already enforced upstream
// by resolveRepo) can wake any role declared in the host yaml — there
// is no per-role actor gate.
func (h *Handler) fireCommentTriggers(r *http.Request, rc *repoCtx, iss *domain.Issue, c *domain.Comment) {
	if h.spawner == nil {
		return
	}
	ctx := r.Context()
	caller, _ := authdomain.UserFromRequest(r)
	if caller == nil {
		// createComment requires auth — this path is defensive.
		return
	}

	// Build the payload the agent's input frame carries. Wire shape:
	// {comment_id, comment_body, author_id, author_name}. Body is
	// included so the agent can read the comment without an extra
	// platform MCP roundtrip.
	payloadBytes, _ := json.Marshal(map[string]any{
		"comment_id":   c.ID,
		"comment_body": c.Body,
		"author_id":    c.AuthorID,
		"author_name":  c.AuthorName,
		"file_path":    c.FilePath,
		"line":         c.Line,
	})
	causeID := strconv.FormatInt(c.ID, 10)

	// Resolve the commenter's identity into a (role_key, user_name)
	// pair so per-role from_roles / from_users filters can match.
	// Comments authored by an agent carry role_key in c.AuthorRoleKey;
	// human comments leave it empty and we use the platform username.
	commentCtx := &agentsessiondomain.CommentContext{
		AuthorRoleKey: c.AgentRole,
		Mentions:      agentsconfig.ParseMentions(c.Body),
	}
	if c.AgentRole == "" {
		commentCtx.AuthorUser = c.AuthorName
	}

	if _, err := h.spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
		Trigger:     agentsconfig.TriggerIssueComment,
		CauseKind:   agentsessiondomain.CauseKindCommentMentioned,
		CauseID:     causeID,
		RepoID:      rc.repo.ID,
		IssueNumber: int32(iss.Number),
		ActorID:     caller.ID,
		Comment:     commentCtx,
		Payload:     payloadBytes,
	}); err != nil {
		// Same rationale as fireIssueOpened: surface whole-config
		// failures (broken agents.yml at the default-branch tip) so the
		// operator can find them in the log without grepping the
		// agent_session module.
		log.Printf("issue: fireCommentTriggers repo=%d issue=%d comment=%d: %v", rc.repo.ID, iss.Number, c.ID, err)
	}
}

type mergeReq struct {
	Message string `json:"message,omitempty"`
}

// merge runs MergeBranch (merge-commit strategy) on the bare repo. Only
// owner or admin may merge. On success the issue transitions to State=merged,
// timeline events are written, sessions are archived, and the issue branch
// is deleted (unless the host config disables it or branch protections
// forbid it).
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
	// Review gate: block merge when review requirements are not satisfied.
	events, gerr := h.issues.ListEvents(r.Context(), iss.ID)
	if gerr != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "cannot evaluate review status")
		return
	}
	rs := domain.ComputeReviewStatus(iss, events)
	if rs.MergeBlocked {
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":         "merge blocked",
			"block_reason":  rs.BlockReason,
			"review_status": rs,
		})
		return
	}

	headSHA, err := h.git.ResolveCommit(rc.fsPath, iss.BranchName)
	if err != nil || headSHA == "" {
		httpx.WriteError(w, http.StatusConflict, "issue branch has no commits yet")
		return
	}
	// Snapshot the base branch tip *before* merging — for fast-forward
	// merges the base is rewritten to the issue tip, so we'd otherwise
	// lose the divergence point and the post-merge commits view would
	// short-circuit to empty.
	preMergeBaseSHA, _ := h.git.ResolveCommit(rc.fsPath, iss.BaseBranch)

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
			httpx.WriteError(w, http.StatusConflict, "merge conflict — resolve conflicts manually")
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
		BaseSHA:    preMergeBaseSHA,
		MergeSHA:   mergeSHA,
		Mode:       mode,
	})
	_, _ = h.issues.CreateEvent(r.Context(), iss.ID, domain.EventBranchMerged, mergePayload, caller.ID)
	statePayload, _ := json.Marshal(domain.StateChangedPayload{From: domain.StateOpen, To: domain.StateMerged})
	_, _ = h.issues.CreateEvent(r.Context(), iss.ID, domain.EventStateChanged, statePayload, caller.ID)

	// Try to delete the issue branch unless the host config disables it.
	cleanup := h.tryDeleteIssueBranch(r.Context(), rc.repo.ID, rc.fsPath, iss.BranchName)

	// Archive every live session on this issue. The parent issue is the
	// only thing that can archive sessions — admin "stop this agent" is
	// "remove the role from host yaml", not a per-session button.
	h.fireIssueClosed(r.Context(), rc.repo.ID, int32(iss.Number))

	resp := map[string]any{
		"issue":     toPublic(updated),
		"merge_sha": mergeSHA,
		"mode":      mode,
	}
	if cleanup != nil {
		resp["cleanup"] = cleanup
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
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
	if err := h.SyncIssueBranch(r.Context(), rc.repo, rc.fsPath, iss, 0, ""); err != nil {
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

// mergeCleanup is the post-merge branch-deletion outcome. nil means "no
// attempt was made" (neither the host config nor protections were consulted).
type mergeCleanup struct {
	Deleted bool   `json:"deleted"`
	Reason  string `json:"reason,omitempty"`
}

// tryDeleteIssueBranch attempts to delete the issue branch after a successful
// merge. It consults the host config first: if delete_branch_on_merge is
// explicitly false the call is a no-op. Otherwise it checks branch protections,
// runs guards, and calls git.DeleteBranch. Failures are recorded in the
// returned cleanup struct but never prevent the merge from succeeding.
func (h *Handler) tryDeleteIssueBranch(ctx context.Context, repoID int64, fsPath, branchName string) *mergeCleanup {
	// Consult host config. Missing yaml = nil config → treat as
	// "defaults apply" (delete_branch_on_merge defaults to true).
	if h.spawner != nil {
		cfg, err := h.spawner.LoadHostConfig(ctx, repoID)
		if err == nil && cfg != nil && cfg.Issues != nil && !cfg.Issues.DeleteBranchOnMerge {
			return nil
		}
	}

	// Check branch protections.
	if h.protections != nil {
		rules, err := h.protections.List(ctx, repoID)
		if err == nil {
			if rule := repodomain.MatchProtection(rules, branchName); rule != nil && rule.ForbidDelete {
				return &mergeCleanup{Deleted: false, Reason: "protected"}
			}
		}
	}

	// Run branch-write guards.
	oldSHA, _ := h.git.ResolveCommit(fsPath, branchName)
	for _, g := range h.guards {
		if err := g.CheckBranchWrite(ctx, repodomain.BranchWriteOp{
			RepoID:     repoID,
			Branch:     branchName,
			OldSHA:     oldSHA,
			IsDelete:   true,
			IsInternal: true,
		}); err != nil {
			return &mergeCleanup{Deleted: false, Reason: "denied"}
		}
	}

	if err := h.git.DeleteBranch(fsPath, branchName); err != nil {
		return &mergeCleanup{Deleted: false, Reason: "delete_failed"}
	}
	return &mergeCleanup{Deleted: true}
}

// mentionSuggestions feeds the comment editor's `@` autocomplete. Returns the
// list of agent role keys declared in `.hangrix/agents.yml` so the editor can
// surface valid `@agent-<role>` mentions. The list is sorted alphabetically so
// repeated calls produce a stable order. Missing / unparseable host yaml is
// not an error here — the dropdown just shows an empty list (a repo without
// agents legitimately has no `@agent-` targets).
type mentionAgent struct {
	RoleKey string `json:"role_key"`
}

type mentionSuggestionsResp struct {
	Agents []mentionAgent `json:"agents"`
	// HostYAMLError is non-empty when `.hangrix/agents.yml` at the
	// default-branch tip fails to parse. Without this field a broken
	// host yaml manifested only as "the dropdown is empty + new issues
	// don't wake any agent", with no clue about why. The UI surfaces
	// this string near the comment editor so the operator knows to
	// push a fix instead of assuming the platform is misbehaving.
	HostYAMLError string `json:"host_yaml_error,omitempty"`
}

func (h *Handler) mentionSuggestions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	out := mentionSuggestionsResp{Agents: []mentionAgent{}}
	if h.spawner != nil {
		cfg, err := h.spawner.LoadHostConfig(r.Context(), rc.repo.ID)
		switch {
		case err != nil:
			out.HostYAMLError = err.Error()
			log.Printf("issue: mentionSuggestions repo=%d host yaml broken: %v", rc.repo.ID, err)
		case cfg != nil:
			keys := make([]string, 0, len(cfg.Roles))
			for k := range cfg.Roles {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				out.Agents = append(out.Agents, mentionAgent{RoleKey: k})
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
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


// --- patch handlers ---

type publicPatch struct {
	ID               int64            `json:"id"`
	IssueID          int64            `json:"issue_id"`
	SessionID        int64            `json:"session_id"`
	AgentRole        string           `json:"agent_role"`
	BaseHeadSHA      string           `json:"base_head_sha"`
	Title            string           `json:"title"`
	Description      string           `json:"description"`
	PatchCount       int32            `json:"patch_count"`
	ChangedPaths     []string         `json:"changed_paths"`
	FileCount        int32            `json:"file_count"`
	Additions        int32            `json:"additions"`
	Deletions        int32            `json:"deletions"`
	Status           string           `json:"status"`
	AppliedCommitSHA string           `json:"applied_commit_sha,omitempty"`
	AppliedAt        *time.Time       `json:"applied_at,omitempty"`
	RejectedReason   string           `json:"rejected_reason,omitempty"`
	ApplyError       string           `json:"apply_error,omitempty"`
	Patches          []publicPatchFile `json:"patches,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

type publicPatchFile struct {
	Seq        int32  `json:"index"`
	FileName   string `json:"file_name"`
	SourcePath string `json:"source_path"`
	PatchText  string `json:"patch_text,omitempty"`
	Subject    string `json:"subject,omitempty"`
}

func toPublicPatch(p *domain.PatchSubmission) publicPatch {
	return publicPatch{
		ID:               p.ID,
		IssueID:          p.IssueID,
		SessionID:        p.SessionID,
		AgentRole:        p.AgentRole,
		BaseHeadSHA:      p.BaseHeadSHA,
		Title:            p.Title,
		Description:      p.Description,
		PatchCount:       p.PatchCount,
		ChangedPaths:     p.ChangedPaths,
		FileCount:        p.FileCount,
		Additions:        p.Additions,
		Deletions:        p.Deletions,
		Status:           string(p.Status),
		AppliedCommitSHA: p.AppliedCommitSHA,
		AppliedAt:        p.AppliedAt,
		RejectedReason:   p.RejectedReason,
		ApplyError:       p.ApplyError,
		CreatedAt:        p.CreatedAt,
		UpdatedAt:        p.UpdatedAt,
	}
}

func (h *Handler) listPatches(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	patches, err := h.patches.ListPatches(r.Context(), iss.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicPatch, 0, len(patches))
	for _, p := range patches {
		items = append(items, toPublicPatch(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"patches": items})
}

func (h *Handler) getPatch(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	rawID := chi.URLParam(r, "patchID")
	patchID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || patchID <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid patch id")
		return
	}
	patch, err := h.patches.GetPatch(r.Context(), patchID)
	if err != nil {
		if errors.Is(err, domain.ErrPatchNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "patch not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if patch.IssueID != iss.ID {
		httpx.WriteError(w, http.StatusNotFound, "patch not found")
		return
	}
	pub := toPublicPatch(patch)
	// Include the patch file series on detail view.
	if patchFiles, err := h.patches.GetPatchFiles(r.Context(), patch.ID); err == nil {
		pub.Patches = make([]publicPatchFile, len(patchFiles))
		for i, pf := range patchFiles {
			pub.Patches[i] = publicPatchFile{
				Seq:        pf.Seq,
				FileName:   pf.FileName,
				SourcePath: pf.FileName,
				PatchText:  pf.PatchText,
				Subject:    pf.Subject,
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, pub)
}

// applyPatch transitions a submitted patch to 'applying' so a dedicated
// apply agent can pick it up and apply via `git am` in its workspace.
// base_head_sha is NOT gated — the apply agent decides via `git am`.
func (h *Handler) applyPatch(w http.ResponseWriter, r *http.Request) {
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
		httpx.WriteError(w, http.StatusForbidden, "only the repo owner can apply patches")
		return
	}

	rawID := chi.URLParam(r, "patchID")
	patchID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || patchID <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid patch id")
		return
	}

	patch, err := h.patches.GetPatch(r.Context(), patchID)
	if err != nil {
		if errors.Is(err, domain.ErrPatchNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "patch not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if patch.IssueID != iss.ID {
		httpx.WriteError(w, http.StatusNotFound, "patch not found")
		return
	}
	if patch.Status != domain.PatchStatusSubmitted {
		httpx.WriteError(w, http.StatusConflict, fmt.Sprintf("cannot apply patch with status '%s' — must be 'submitted'", patch.Status))
		return
	}

	// Transition to applying so the apply agent can pick it up.
	updated, err := h.patches.MarkApplying(r.Context(), patch.ID)
	if err != nil {
		if errors.Is(err, domain.ErrPatchNotSubmitted) {
			httpx.WriteError(w, http.StatusConflict, "patch is not in 'submitted' state — may have been claimed by another apply request")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "mark applying: "+err.Error())
		return
	}

	payload, _ := json.Marshal(domain.PatchEventPayload{
		SubmissionID: updated.ID,
		Title:        updated.Title,
		AgentRole:    updated.AgentRole,
	})
	_, _ = h.issues.CreateEvent(r.Context(), iss.ID, domain.EventPatchApplying, payload, caller.ID)

	// Fire patch.apply_requested trigger to wake the apply agent.
	if h.spawner != nil {
		triggerPayload, _ := json.Marshal(map[string]any{
			"submission_id": updated.ID,
			"issue_number":  iss.Number,
		})
		_, _ = h.spawner.OnTrigger(r.Context(), agentsessiondomain.TriggerInput{
			Trigger:     agentsconfig.TriggerPatchApplyRequested,
			CauseKind:   agentsessiondomain.CauseKindPatchApplyRequested,
			CauseID:     strconv.FormatInt(updated.ID, 10),
			RepoID:      rc.repo.ID,
			IssueNumber: int32(iss.Number),
			ActorID:     caller.ID,
			Payload:     triggerPayload,
		})
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id":      updated.ID,
		"status":  string(updated.Status),
		"message": "apply request accepted — a dedicated apply agent will pick up this submission",
	})
}

// rejectPatch rejects a submitted or applying patch without applying it.
// Only the repo owner/admin may reject patches.
func (h *Handler) rejectPatch(w http.ResponseWriter, r *http.Request) {
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
		httpx.WriteError(w, http.StatusForbidden, "only the repo owner can reject patches")
		return
	}

	rawID := chi.URLParam(r, "patchID")
	patchID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || patchID <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid patch id")
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		httpx.WriteError(w, http.StatusBadRequest, "reason is required")
		return
	}

	patch, err := h.patches.GetPatch(r.Context(), patchID)
	if err != nil {
		if errors.Is(err, domain.ErrPatchNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "patch not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if patch.IssueID != iss.ID {
		httpx.WriteError(w, http.StatusNotFound, "patch not found")
		return
	}
	if patch.Status != domain.PatchStatusSubmitted && patch.Status != domain.PatchStatusApplying {
		httpx.WriteError(w, http.StatusConflict, fmt.Sprintf("cannot reject patch with status '%s' — must be 'submitted' or 'applying'", patch.Status))
		return
	}

	updated, err := h.patches.UpdatePatchStatus(r.Context(), patch.ID, domain.PatchStatusRejected, "", reason)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "update patch status: "+err.Error())
		return
	}

	payload, _ := json.Marshal(domain.PatchEventPayload{
		SubmissionID: updated.ID,
		Title:        updated.Title,
		AgentRole:    updated.AgentRole,
		Reason:       reason,
	})
	_, _ = h.issues.CreateEvent(r.Context(), iss.ID, domain.EventPatchRejected, payload, caller.ID)

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id":     updated.ID,
		"status": string(updated.Status),
		"reason": updated.RejectedReason,
	})
}

// voidPatch voids a submitted or applying patch submission. Only the repo
// owner/admin may void patches. Used by maintainers to cancel a patch that
// is no longer valid. Records a patch_voided event.
func (h *Handler) voidPatch(w http.ResponseWriter, r *http.Request) {
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
		httpx.WriteError(w, http.StatusForbidden, "only the repo owner can void patches")
		return
	}

	rawID := chi.URLParam(r, "patchID")
	patchID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || patchID <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid patch id")
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "voided by maintainer"
	}

	patch, err := h.patches.GetPatch(r.Context(), patchID)
	if err != nil {
		if errors.Is(err, domain.ErrPatchNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "patch not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if patch.IssueID != iss.ID {
		httpx.WriteError(w, http.StatusNotFound, "patch not found")
		return
	}
	if patch.Status != domain.PatchStatusSubmitted && patch.Status != domain.PatchStatusApplying {
		httpx.WriteError(w, http.StatusConflict, fmt.Sprintf("cannot void patch with status '%s' — must be 'submitted' or 'applying'", patch.Status))
		return
	}

	updated, err := h.patches.UpdatePatchStatus(r.Context(), patch.ID, domain.PatchStatusVoided, "", reason)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "update patch status: "+err.Error())
		return
	}

	payload, _ := json.Marshal(domain.PatchEventPayload{
		SubmissionID: updated.ID,
		Title:        updated.Title,
		AgentRole:    updated.AgentRole,
		Reason:       reason,
	})
	_, _ = h.issues.CreateEvent(r.Context(), iss.ID, domain.EventPatchVoided, payload, caller.ID)

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id":     updated.ID,
		"status": string(updated.Status),
	})
}

