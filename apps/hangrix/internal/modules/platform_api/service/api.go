package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/hangrix/hangrix/pkg/actor"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	attachmentdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	questionnairedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/questionnaire/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	workflowdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
)

// APIService wraps Registry to expose typed REST-friendly methods that
// satisfy the handler.AgentAPI interface. Each method receives a Actor
// (derived from the hgxs_ session token) and returns typed DTOs + errors
// instead of the legacy Result{Text, IsError} pattern.
type APIService struct {
	r *Registry
}

type APIServiceDeps struct {
	Registry *Registry
}

// NewAPIService creates an APIService backed by the given Registry.
func NewAPIService(deps *APIServiceDeps) *APIService {
	return &APIService{r: deps.Registry}
}

// ---- Helpers ----

func timeNow() time.Time { return time.Now() }

func agentSessionIdentity(roleKey string) agentsessiondomain.CommitIdentity {
	return agentsessiondomain.IdentityForRole(roleKey, "")
}

func gitSignature(id agentsessiondomain.CommitIdentity) gitdomain.Signature {
	return gitdomain.Signature{Name: id.Name, Email: id.Email, When: timeNow()}
}

func actorRef(roleKey string) actor.Ref {
	return actor.AgentRef(roleKey)
}

func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// ---- scope helpers ----

func (s *APIService) loadScope(ctx context.Context, p *apidomain.Actor) (*sessionScope, error) {
	if !p.InRepo() || !p.InIssue() {
		return nil, errors.New("session has no (repo, issue) scope")
	}
	return s.r.loadScope(ctx, p.Session)
}

func (s *APIService) mustLoadScope(ctx context.Context, p *apidomain.Actor) (*sessionScope, error) {
	sc, err := s.loadScope(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("session scope: %w", err)
	}
	return sc, nil
}

// ---- DTO types used by the API service ----

type apiIssueResponse struct {
	Number       int64        `json:"number"`
	Title        string       `json:"title"`
	Body         string       `json:"body"`
	State        string       `json:"state"`
	BaseBranch   string       `json:"base_branch"`
	BranchName   string       `json:"branch_name"`
	HeadSHA      string       `json:"head_sha"`
	Author       string       `json:"author_username"`
	ParentNumber int64        `json:"parent_number,omitempty"`
	CreatedAt    string       `json:"created_at"`
	Comments     []apiComment `json:"comments,omitempty"`
	Events       []apiEvent   `json:"events,omitempty"`
	Todos        []apiTodo    `json:"todos,omitempty"`
	TodoSummary  *apiTodoSum  `json:"todo_summary,omitempty"`
}

type apiComment struct {
	ID        int64  `json:"id"`
	Author    string `json:"author"`
	AgentRole string `json:"agent_role,omitempty"`
	Body      string `json:"body"`
	FilePath  string `json:"file_path,omitempty"`
	Line      int    `json:"line,omitempty"`
	CreatedAt string `json:"created_at"`
}

type apiEvent struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor,omitempty"`
	AgentRole string          `json:"agent_role,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

type apiTodo struct {
	ID        int64  `json:"id"`
	IssueID   int64  `json:"issue_id"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	Position  int    `json:"position"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type apiTodoSum struct {
	Total      int64 `json:"total"`
	Todo       int64 `json:"todo"`
	InProgress int64 `json:"in_progress"`
	Done       int64 `json:"done"`
	AllDone    bool  `json:"all_done"`
}

type apiChild struct {
	ID     int64  `json:"id"`
	Number int64  `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
}

type apiContribItem struct {
	ID              int64          `json:"id"`
	IssueID         int64          `json:"issue_id"`
	AgentRole       string         `json:"agent_role"`
	Actor           map[string]any `json:"actor"`
	RefName         string         `json:"ref_name"`
	HeadSHA         string         `json:"head_sha"`
	BaseSHA         string         `json:"base_sha"`
	Title           string         `json:"title"`
	Description     string         `json:"description"`
	Status          string         `json:"status"`
	Mergeable       bool           `json:"mergeable"`
	MergeMode       string         `json:"merge_mode"`
	ChangedPaths    []string       `json:"changed_paths"`
	Files           int32          `json:"files"`
	Additions       int32          `json:"additions"`
	Deletions       int32          `json:"deletions"`
	MergedCommitSHA string         `json:"merged_commit_sha,omitempty"`
	MergedAt        string         `json:"merged_at,omitempty"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
}

type apiSessionItem struct {
	RoleKey        string `json:"role_key"`
	Status         string `json:"status"`
	RepoSHA        string `json:"repo_sha"`
	CreatedAt      string `json:"created_at"`
	LastActivityAt string `json:"last_activity_at"`
}

type apiReleaseItem struct {
	ID              int64  `json:"id"`
	TagName         string `json:"tag_name"`
	TargetCommitSHA string `json:"target_commit_sha"`
	Title           string `json:"title"`
	IsDraft         bool   `json:"is_draft"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	PublishedAt     string `json:"published_at,omitempty"`
}

// ---- Identity ----

// GetMe returns the authenticated agent's identity.
func (s *APIService) GetMe(ctx context.Context, p *apidomain.Actor) (any, error) {
	active := p.Session.SessionTokenActive(timeNow())
	return map[string]any{
		"session_id":     p.SessionID,
		"role_key":       p.RoleKey,
		"repo_id":        p.RepoID,
		"issue_number":   p.IssueNumber,
		"session_status": string(p.Session.Status),
		"token_active":   active,
	}, nil
}

// ---- Issue ----

// ReadIssue returns the current issue with comments, events, and todos.
func (s *APIService) ReadIssue(ctx context.Context, p *apidomain.Actor) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	comments, err := s.r.deps.Issues.ListComments(ctx, scope.issue.ID)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	events, err := s.r.deps.Issues.ListEvents(ctx, scope.issue.ID)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	todos, todoSummary, _ := s.r.loadTodos(ctx, scope.issue.ID)

	return &apiIssueResponse{
		Number:       scope.issue.Number,
		Title:        scope.issue.Title,
		Body:         scope.issue.Body,
		State:        string(scope.issue.State),
		BaseBranch:   scope.issue.BaseBranch,
		BranchName:   scope.issue.BranchName,
		HeadSHA:      scope.issue.HeadSHA,
		Author:       scope.issue.AuthorName,
		ParentNumber: scope.issue.ParentNumber,
		CreatedAt:    stableTime(scope.issue.CreatedAt),
		Comments:     commentsToAPIDTO(comments),
		Events:       eventsToAPIDTO(events),
		Todos:        todosToAPIDTO(todos),
		TodoSummary:  todoSummaryToAPIDTO(todoSummary),
	}, nil
}

// EditIssue updates the current issue's title and/or body.
func (s *APIService) EditIssue(ctx context.Context, p *apidomain.Actor, title, body *string) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	newTitle := scope.issue.Title
	if title != nil {
		newTitle = strings.TrimSpace(*title)
		if newTitle == "" || len(newTitle) > 200 {
			return nil, errors.New("title is required (1-200 chars)")
		}
	}
	newBody := scope.issue.Body
	if body != nil {
		newBody = *body
	}
	titleChanged := newTitle != scope.issue.Title

	updated, err := s.r.deps.Issues.UpdateTitleBody(ctx, scope.issue.ID, newTitle, newBody)
	if err != nil {
		return nil, fmt.Errorf("update issue: %w", err)
	}
	if titleChanged {
		payload, _ := json.Marshal(issuedomain.TitleChangedPayload{
			From: scope.issue.Title, To: newTitle,
		})
		_, _ = s.r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventTitleChanged, payload, p.RoleKey)
	}
	return map[string]any{
		"title":         updated.Title,
		"body":          updated.Body,
		"title_changed": titleChanged,
	}, nil
}

// ReadIssueByNumber reads any issue in the same repo by number.
func (s *APIService) ReadIssueByNumber(ctx context.Context, p *apidomain.Actor, issueNumber int64) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	iss, err := s.r.deps.Issues.GetByNumber(ctx, scope.repo.ID, issueNumber)
	if err != nil {
		if errors.Is(err, issuedomain.ErrIssueNotFound) {
			return nil, errors.New("issue not found or out of scope")
		}
		return nil, fmt.Errorf("load issue: %w", err)
	}
	comments, _ := s.r.deps.Issues.ListComments(ctx, iss.ID)
	events, _ := s.r.deps.Issues.ListEvents(ctx, iss.ID)
	todos, todoSummary, _ := s.r.loadTodos(ctx, iss.ID)

	return &apiIssueResponse{
		Number:       iss.Number,
		Title:        iss.Title,
		Body:         iss.Body,
		State:        string(iss.State),
		BaseBranch:   iss.BaseBranch,
		BranchName:   iss.BranchName,
		HeadSHA:      iss.HeadSHA,
		Author:       iss.AuthorName,
		ParentNumber: iss.ParentNumber,
		CreatedAt:    stableTime(iss.CreatedAt),
		Comments:     commentsToAPIDTO(comments),
		Events:       eventsToAPIDTO(events),
		Todos:        todosToAPIDTO(todos),
		TodoSummary:  todoSummaryToAPIDTO(todoSummary),
	}, nil
}

// CreateIssue creates a new issue, optionally as a child of the current one.
func (s *APIService) CreateIssue(ctx context.Context, p *apidomain.Actor, title, body string, parent bool) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 200 {
		return nil, errors.New("title is required (1-200 chars)")
	}
	baseBranch := scope.repo.DefaultBranch
	var parentID, parentNumber int64
	if parent {
		baseBranch = scope.issue.BranchName
		parentID = scope.issue.ID
		parentNumber = scope.issue.Number
	}
	iss, err := s.r.deps.Issues.Create(ctx, scope.repo.ID, 0, "", title, body, baseBranch, p.RoleKey, parentID, parentNumber)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}

	// Create the branch ref in the bare repo so it appears in branch
	// listings immediately — before anyone pushes. The new branch points
	// at the base branch's tip, so a subsequent push is a normal
	// fast-forward. Mirrors the issue handler's create().
	if err := s.r.deps.Git.CreateBranch(scope.fsPath, iss.BranchName, baseBranch); err != nil {
		return nil, fmt.Errorf("create branch ref: %w", err)
	}

	// Fire issue.opened so any role whose triggers include issue.opened
	// wakes on its own. Failures don't block issue creation — the
	// operator repairs the host yaml then nudges the issue. Mirrors the
	// issue handler's fireIssueOpened().
	if s.r.deps.Spawner != nil {
		if _, err := s.r.deps.Spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
			Trigger:     agentsconfig.TriggerIssueOpened,
			CauseKind:   agentsessiondomain.CauseKindIssueOpened,
			CauseID:     "",
			RepoID:      scope.repo.ID,
			IssueNumber: int32(iss.Number),
			ActorID:     0, // agent-created issues have no user actor
		}); err != nil {
			log.Printf("platform_api: fire issue.opened repo=%d issue=%d: %v", scope.repo.ID, iss.Number, err)
		}
	}

	return map[string]any{
		"number":      iss.Number,
		"title":       iss.Title,
		"state":       string(iss.State),
		"branch_name": iss.BranchName,
	}, nil
}

// ---- Comments ----

// CreateComment posts an agent-authored comment on the current issue.
func (s *APIService) CreateComment(ctx context.Context, p *apidomain.Actor, body, filePath string, line int) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("body is required")
	}
	c, err := s.r.deps.Issues.CreateAgentComment(ctx, scope.issue.ID, p.RoleKey, body, strings.TrimSpace(filePath), line)
	if err != nil {
		return nil, fmt.Errorf("create comment: %w", err)
	}
	s.r.fanCommentMentions(ctx, p.Session, scope, c)
	return map[string]any{
		"id":         c.ID,
		"agent_role": c.AgentRole,
		"created_at": stableTime(c.CreatedAt),
	}, nil
}

// GetComment reads a single comment by id.
func (s *APIService) GetComment(ctx context.Context, p *apidomain.Actor, commentID int64) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	c, err := s.r.deps.Issues.GetCommentByID(ctx, commentID)
	if err != nil {
		return nil, errors.New("comment not found")
	}
	if c.IssueID != scope.issue.ID {
		return nil, errors.New("comment not found or out of scope")
	}
	author := c.AuthorName
	if c.AgentRole != "" {
		author = c.AgentRole
	}
	return &apiComment{
		ID:        c.ID,
		Author:    author,
		AgentRole: c.AgentRole,
		Body:      c.Body,
		FilePath:  c.FilePath,
		Line:      c.Line,
		CreatedAt: stableTime(c.CreatedAt),
	}, nil
}

// ---- Children / Checks ----

// ListChildren lists sub-issues of the current issue.
func (s *APIService) ListChildren(ctx context.Context, p *apidomain.Actor) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	kids, err := s.r.deps.Issues.ListChildren(ctx, scope.issue.ID)
	if err != nil {
		return nil, fmt.Errorf("list children: %w", err)
	}
	items := make([]apiChild, 0, len(kids))
	for _, k := range kids {
		items = append(items, apiChild{
			ID: k.ID, Number: k.Number, Title: k.Title, State: string(k.State),
		})
	}
	return items, nil
}

// ListChecks returns CI check statuses for the current issue's head commit.
func (s *APIService) ListChecks(ctx context.Context, p *apidomain.Actor) (any, error) {
	sc, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if sc.issue.HeadSHA == "" {
		return []any{}, nil
	}
	if s.r.deps.CheckReader == nil {
		return []any{}, nil
	}
	items, err := s.r.deps.CheckReader.ListChecksByCommit(ctx, sc.repo.ID, sc.issue.HeadSHA)
	if err != nil {
		return nil, fmt.Errorf("list checks: %w", err)
	}
	if items == nil {
		items = []workflowdomain.CheckItem{}
	}
	return items, nil
}

// ---- Todos ----

// ListTodos returns todos for the current issue.
func (s *APIService) ListTodos(ctx context.Context, p *apidomain.Actor) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	todos, summary, err := s.r.loadTodos(ctx, scope.issue.ID)
	if err != nil {
		return nil, fmt.Errorf("load todos: %w", err)
	}
	return map[string]any{
		"todos":   todosToAPIDTO(todos),
		"summary": todoSummaryToAPIDTO(summary),
	}, nil
}

// CreateTodo creates a new todo on the current issue.
func (s *APIService) CreateTodo(ctx context.Context, p *apidomain.Actor, content, status string, position int) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	st := issuedomain.TodoStatus(strings.TrimSpace(status))
	if st == "" {
		st = issuedomain.TodoStatusTodo
	}
	if !st.Valid() {
		return nil, errors.New("status must be todo|in_progress|done")
	}
	todo, err := s.r.deps.Todos.CreateTodo(ctx, scope.issue.ID, strings.TrimSpace(content), st, position)
	if err != nil {
		return nil, fmt.Errorf("create todo: %w", err)
	}
	return todoToAPIDTO(todo), nil
}

// UpdateTodo updates an existing todo's status and/or content.
func (s *APIService) UpdateTodo(ctx context.Context, p *apidomain.Actor, todoID int64, status string, content *string) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	existing, err := s.r.deps.Todos.GetTodo(ctx, todoID)
	if err != nil {
		return nil, fmt.Errorf("get todo: %w", err)
	}
	if existing.IssueID != scope.issue.ID {
		return nil, errors.New("todo does not belong to the current issue")
	}
	st := issuedomain.TodoStatus(strings.TrimSpace(status))
	if st == "" {
		st = existing.Status
	}
	if !st.Valid() {
		return nil, errors.New("status must be todo|in_progress|done")
	}
	var contentPtr *string
	if content != nil && strings.TrimSpace(*content) != "" {
		c := strings.TrimSpace(*content)
		contentPtr = &c
	}
	todo, err := s.r.deps.Todos.UpdateTodoStatus(ctx, todoID, st, contentPtr)
	if err != nil {
		return nil, fmt.Errorf("update todo: %w", err)
	}
	return todoToAPIDTO(todo), nil
}

// ---- Contributions ----

// ListContributions lists contributions on the current issue.
func (s *APIService) ListContributions(ctx context.Context, p *apidomain.Actor, includeClosed, includeMerged bool) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	contribs, err := s.r.deps.Contributions.ListContributions(ctx, scope.issue.ID, includeClosed, includeMerged)
	if err != nil {
		return nil, fmt.Errorf("list contributions: %w", err)
	}
	items := make([]apiContribItem, 0, len(contribs))
	for _, c := range contribs {
		items = append(items, contributionSummaryToAPI(c))
	}
	return items, nil
}

// ReadContribution returns full contribution detail with review status.
func (s *APIService) ReadContribution(ctx context.Context, p *apidomain.Actor, id int64) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	c, err := s.r.deps.Contributions.GetContribution(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get contribution: %w", err)
	}
	if c.IssueID != scope.issue.ID {
		return nil, errors.New("contribution does not belong to the current issue")
	}
	contribBranch := strings.TrimPrefix(c.RefName, "refs/heads/")
	checkoutHint := fmt.Sprintf(
		"To view the changes locally, fetch the contribution branch and compare with the issue branch:\n\n  git fetch origin %s\n  git diff origin/%s...origin/%s\n\nOr checkout directly:\n\n  git fetch origin %s && git checkout %s",
		contribBranch, scope.issue.BranchName, contribBranch,
		contribBranch, contribBranch,
	)
	var review any
	if events, err := s.r.deps.Issues.ListEvents(ctx, scope.issue.ID); err == nil {
		review = issuedomain.ComputeContributionReviewStatus(c, s.r.requiredReviewers(ctx, scope.repo.ID, c), events)
	}
	return map[string]any{
		"contribution":  contributionSummaryToAPI(c),
		"review":        review,
		"checkout_hint": checkoutHint,
	}, nil
}

// SetContributionMeta sets the title/description of the owner's contribution.
func (s *APIService) SetContributionMeta(ctx context.Context, p *apidomain.Actor, id int64, title, description string) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 200 {
		return nil, errors.New("title is required (1-200 chars)")
	}
	c, err := s.r.deps.Contributions.GetContribution(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get contribution: %w", err)
	}
	if c.IssueID != scope.issue.ID {
		return nil, errors.New("contribution does not belong to the current issue")
	}
	if c.AgentRole != p.RoleKey {
		return nil, errors.New("only the owning role can set this contribution's metadata")
	}
	updated, err := s.r.deps.Contributions.SetContributionMeta(ctx, c.ID, title, strings.TrimSpace(description))
	if err != nil {
		return nil, fmt.Errorf("set meta: %w", err)
	}
	return contributionSummaryToAPI(updated), nil
}

// ApplyContribution merges an approved contribution into the issue branch.
func (s *APIService) ApplyContribution(ctx context.Context, p *apidomain.Actor, id int64, message string) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	c, err := s.r.deps.Contributions.GetContribution(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get contribution: %w", err)
	}
	if c.IssueID != scope.issue.ID {
		return nil, errors.New("contribution does not belong to the current issue")
	}
	if c.Status.Terminal() {
		return nil, fmt.Errorf("contribution is %s", c.Status)
	}
	events, err := s.r.deps.Issues.ListEvents(ctx, scope.issue.ID)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	if rs := issuedomain.ComputeContributionReviewStatus(c, s.r.requiredReviewers(ctx, scope.repo.ID, c), events); rs.MergeBlocked {
		return nil, fmt.Errorf("merge blocked: %s", rs.BlockReason)
	}
	contribBranch := strings.TrimPrefix(c.RefName, "refs/heads/")
	mergeable, mode, hint, _ := s.r.deps.Git.CheckAutoMerge(scope.fsPath, scope.issue.BranchName, contribBranch)
	if !mergeable {
		_ = s.r.deps.Contributions.SetContributionMergeable(ctx, c.ID, false, mode)
		return nil, fmt.Errorf("contribution is not mergeable: %s", hint)
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = fmt.Sprintf("Merge contribution %s into %s (issue #%d)", contribBranch, scope.issue.BranchName, scope.issue.Number)
	}
	identity := agentSessionIdentity(p.RoleKey)
	mergeSHA, mergedMode, err := s.r.deps.Git.MergeBranch(scope.fsPath, scope.issue.BranchName, contribBranch, msg, gitSignature(identity))
	if err != nil {
		if errors.Is(err, gitdomain.ErrMergeConflict) {
			_ = s.r.deps.Contributions.SetContributionMergeable(ctx, c.ID, false, "conflicted")
			return nil, fmt.Errorf("merge conflict — contributor must rebase onto the latest issue/%d and push a NEW slug", scope.issue.Number)
		}
		return nil, fmt.Errorf("merge: %w", err)
	}
	_ = s.r.deps.Issues.UpdateHeadSHA(ctx, scope.issue.ID, mergeSHA)
	merged, err := s.r.deps.Contributions.MarkContributionMerged(ctx, c.ID, mergeSHA)
	if err != nil {
		return nil, fmt.Errorf("mark merged: %w", err)
	}
	evtPayload, _ := json.Marshal(issuedomain.ContributionEventPayload{
		ContributionID: merged.ID, AgentRole: merged.AgentRole, RefName: merged.RefName,
		Title: merged.Title, MergeCommitSHA: mergeSHA,
	})
	_, _ = s.r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventContributionMerged, evtPayload, p.RoleKey)
	s.r.refreshSiblingMergeability(ctx, scope, merged.ID)
	return map[string]any{
		"id": merged.ID, "status": string(merged.Status), "merge_sha": mergeSHA, "mode": mergedMode,
	}, nil
}

// CloseContribution abandons the owner's contribution branch.
func (s *APIService) CloseContribution(ctx context.Context, p *apidomain.Actor, id int64, reason string) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	c, err := s.r.deps.Contributions.GetContribution(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get contribution: %w", err)
	}
	if c.IssueID != scope.issue.ID {
		return nil, errors.New("contribution does not belong to the current issue")
	}
	if c.AgentRole != p.RoleKey {
		return nil, errors.New("only the owning role can close this contribution")
	}
	if c.Status.Terminal() {
		return nil, fmt.Errorf("contribution is %s", c.Status)
	}
	updated, err := s.r.deps.Contributions.SetContributionStatus(ctx, c.ID, issuedomain.ContribStatusClosed)
	if err != nil {
		return nil, fmt.Errorf("close: %w", err)
	}
	evtPayload, _ := json.Marshal(issuedomain.ContributionEventPayload{
		ContributionID: updated.ID, AgentRole: updated.AgentRole, RefName: updated.RefName, Reason: strings.TrimSpace(reason),
	})
	_, _ = s.r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventContributionClosed, evtPayload, p.RoleKey)
	return map[string]any{"id": updated.ID, "status": string(updated.Status)}, nil
}

// ---- Reviews ----

// CreateReview casts a review vote on a contribution.
func (s *APIService) CreateReview(ctx context.Context, p *apidomain.Actor, contributionID int64, value, reason string) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	v := issuedomain.ReviewVoteValue(strings.TrimSpace(value))
	if !v.Valid() {
		return nil, errors.New("value must be approve|reject|abstain")
	}
	if contributionID <= 0 {
		return nil, errors.New("contribution_id is required")
	}
	c, err := s.r.deps.Contributions.GetContribution(ctx, contributionID)
	if err != nil {
		return nil, fmt.Errorf("get contribution: %w", err)
	}
	if c.IssueID != scope.issue.ID {
		return nil, errors.New("contribution does not belong to the current issue")
	}
	if c.HeadSHA == "" {
		return nil, errors.New("contribution branch has no commits yet")
	}
	if v == issuedomain.ReviewVoteApprove && c.AgentRole == p.RoleKey {
		return nil, errors.New("you cannot approve your own contribution")
	}
	payload := issuedomain.ReviewVotePayload{
		Value: v, Reason: reason, ContributionID: c.ID, ReviewedSHA: c.HeadSHA,
	}
	payloadBytes, _ := json.Marshal(payload)
	evt, err := s.r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventReviewVote, payloadBytes, p.RoleKey)
	if err != nil {
		return nil, fmt.Errorf("create vote event: %w", err)
	}
	if v == issuedomain.ReviewVoteReject {
		reqPayload, _ := json.Marshal(issuedomain.ContributionEventPayload{
			ContributionID: c.ID, AgentRole: c.AgentRole, RefName: c.RefName, Reason: reason,
		})
		_, _ = s.r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventContributionRejected, reqPayload, p.RoleKey)
	}
	s.r.recomputeContributionStatus(ctx, scope.repo.ID, c)
	return map[string]any{"event_id": evt.ID, "value": string(v)}, nil
}

// ---- Merge / Close ----

// GetMergeability checks if the issue branch is mergeable.
func (s *APIService) GetMergeability(ctx context.Context, p *apidomain.Actor) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	iss := scope.issue
	if iss.HeadSHA == "" {
		return map[string]any{
			"mergeable":   false,
			"mode":        "unknown",
			"base_branch": iss.BaseBranch,
			"hint":        "issue branch has no commits yet",
		}, nil
	}
	baseSHA, err := s.r.deps.Git.ResolveCommit(scope.fsPath, iss.BaseBranch)
	if err != nil {
		return map[string]any{
			"mergeable":   false,
			"mode":        "unknown",
			"base_branch": iss.BaseBranch,
			"head_sha":    iss.HeadSHA,
			"hint":        "base branch not found",
		}, nil
	}
	mergeable, mode, hint, err := s.r.deps.Git.CheckAutoMerge(scope.fsPath, iss.BaseBranch, iss.HeadSHA)
	if err != nil {
		return nil, fmt.Errorf("check auto-merge: %w", err)
	}
	if mode == "conflicted" {
		hint = fmt.Sprintf(
			"merge would conflict with `%s` — create a new contribution branch from the latest `issue/%d`, resolve the conflict there, push it to `refs/heads/issue-%d/%s/<slug>`, then land it via the contribution review/apply flow; do not push the issue branch directly",
			iss.BaseBranch, iss.Number, iss.Number, p.RoleKey,
		)
	}
	if block := s.r.issueMergeBlock(ctx, scope); block != "" {
		mergeable = false
		mode = "blocked"
		hint = block
	}
	var incompleteTodos []apiTodo
	if mergeable {
		block, incTodos := s.r.todosCompletionBlock(ctx, scope)
		if block != "" {
			mergeable = false
			mode = "blocked"
			hint = block
			for _, t := range incTodos {
				incompleteTodos = append(incompleteTodos, apiTodo{
					ID:      t["id"].(int64),
					Content: t["content"].(string),
					Status:  t["status"].(string),
				})
			}
		}
	}
	var incompleteSubIssues []map[string]any
	if mergeable {
		block, openDesc := s.r.subIssueBlock(ctx, scope)
		if block != "" {
			mergeable = false
			mode = "blocked"
			hint = block
			for _, d := range openDesc {
				incompleteSubIssues = append(incompleteSubIssues, map[string]any{
					"id":     d.ID,
					"number": d.Number,
					"title":  d.Title,
					"depth":  d.Depth,
				})
			}
		}
	}
	result := map[string]any{
		"mergeable":   mergeable,
		"mode":        mode,
		"base_branch": iss.BaseBranch,
		"base_sha":    baseSHA,
		"head_sha":    iss.HeadSHA,
		"hint":        hint,
	}
	if len(incompleteTodos) > 0 {
		result["incomplete_todos"] = incompleteTodos
	}
	if len(incompleteSubIssues) > 0 {
		result["incomplete_sub_issues"] = incompleteSubIssues
	}
	return result, nil
}

// MergeIssue merges the issue branch into base.
func (s *APIService) MergeIssue(ctx context.Context, p *apidomain.Actor, message string) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if scope.issue.State != issuedomain.StateOpen {
		return nil, fmt.Errorf("issue is %s, not open", scope.issue.State)
	}
	if block := s.r.issueMergeBlock(ctx, scope); block != "" {
		return nil, fmt.Errorf("merge blocked: %s", block)
	}
	if block, _ := s.r.todosCompletionBlock(ctx, scope); block != "" {
		return nil, fmt.Errorf("merge blocked: %s", block)
	}
	if block, openDesc := s.r.subIssueBlock(ctx, scope); block != "" {
		return nil, &issuedomain.BlockError{
			Code:      "incomplete_sub_issues",
			Message:   "merge blocked: " + block,
			SubIssues: openDesc,
		}
	}
	headSHA, err := s.r.deps.Git.ResolveCommit(scope.fsPath, scope.issue.BranchName)
	if err != nil || headSHA == "" {
		return nil, errors.New("issue branch has no commits yet")
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = fmt.Sprintf("Merge issue #%d: %s", scope.issue.Number, scope.issue.Title)
	}
	identity := agentSessionIdentity(p.RoleKey)
	mergeSHA, mode, err := s.r.deps.Git.MergeBranch(scope.fsPath, scope.issue.BaseBranch, scope.issue.BranchName, msg, gitSignature(identity))
	if err != nil {
		if errors.Is(err, gitdomain.ErrMergeConflict) {
			return nil, fmt.Errorf("merge conflict with `%s` — the issue branch can't be pushed directly. Resolve on a new contribution branch off the latest `issue/%d` (`git fetch origin && git rebase origin/issue/%d`), land it via the review/apply flow, then retry.", scope.issue.BaseBranch, scope.issue.Number, scope.issue.Number)
		}
		return nil, fmt.Errorf("merge: %w", err)
	}
	if _, err := s.r.deps.Issues.UpdateState(ctx, scope.issue.ID, issuedomain.StateMerged, mergeSHA); err != nil {
		return nil, fmt.Errorf("update state: %w", err)
	}
	mergePayload, _ := json.Marshal(issuedomain.BranchMergedPayload{
		IntoBranch: scope.issue.BaseBranch, FromBranch: scope.issue.BranchName,
		MergeSHA: mergeSHA, Mode: mode,
	})
	_, _ = s.r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventBranchMerged, mergePayload, p.RoleKey)
	statePayload, _ := json.Marshal(issuedomain.StateChangedPayload{
		From: issuedomain.StateOpen, To: issuedomain.StateMerged,
	})
	_, _ = s.r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventStateChanged, statePayload, p.RoleKey)
	s.r.tryDeleteIssueBranch(ctx, scope.repo.ID, scope.fsPath, scope.issue.BranchName)
	s.r.tryDeleteContributionBranches(ctx, scope.repo.ID, scope.fsPath, scope.issue.Number, scope.issue.ID)
	return map[string]any{"merge_sha": mergeSHA, "mode": mode}, nil
}

// CloseIssue closes the current issue without merging.
func (s *APIService) CloseIssue(ctx context.Context, p *apidomain.Actor, reason string) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if scope.issue.State != issuedomain.StateOpen {
		return map[string]any{"state": string(scope.issue.State), "changed": false}, nil
	}
	if block, _ := s.r.todosCompletionBlock(ctx, scope); block != "" {
		return nil, fmt.Errorf("close blocked: %s", block)
	}
	if block, openDesc := s.r.subIssueBlock(ctx, scope); block != "" {
		return nil, &issuedomain.BlockError{
			Code:      "incomplete_sub_issues",
			Message:   "close blocked: " + block,
			SubIssues: openDesc,
		}
	}
	next, err := s.r.deps.Issues.UpdateState(ctx, scope.issue.ID, issuedomain.StateClosed, "")
	if err != nil {
		return nil, fmt.Errorf("update state: %w", err)
	}
	payload, _ := json.Marshal(issuedomain.StateChangedPayload{
		From: scope.issue.State, To: issuedomain.StateClosed,
	})
	_, _ = s.r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventStateChanged, payload, p.RoleKey)
	return map[string]any{"state": string(next.State), "changed": true}, nil
}

// ---- Sessions ----

// ListSessions lists active sessions on the current issue.
func (s *APIService) ListSessions(ctx context.Context, p *apidomain.Actor) (any, error) {
	if p.RepoID == nil || p.IssueNumber == nil {
		return nil, errors.New("session has no (repo, issue) scope")
	}
	rows, err := s.r.deps.Runner.ListSessionsByIssue(ctx, *p.RepoID, *p.IssueNumber)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	items := make([]apiSessionItem, 0, len(rows))
	for _, sess := range rows {
		items = append(items, apiSessionItem{
			RoleKey:        sess.RoleKey,
			Status:         string(sess.Status),
			RepoSHA:        sess.RepoSHA,
			CreatedAt:      stableTime(sess.CreatedAt),
			LastActivityAt: stableTime(lastActivityAt(sess)),
		})
	}
	return items, nil
}

// RecoverSession recovers a terminal/idle session to pending.
func (s *APIService) RecoverSession(ctx context.Context, p *apidomain.Actor, sessionID int64) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	target, err := s.r.deps.Runner.GetSessionByID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	if target.RepoID == nil || *target.RepoID != scope.repo.ID {
		return nil, errors.New("target session not in same repo")
	}
	if target.IssueNumber == nil || *target.IssueNumber != *p.IssueNumber {
		return nil, errors.New("target session not in same issue")
	}
	switch target.Status {
	case runnerdomain.SessionStatusArchived:
		return nil, errors.New("session is archived, not resumable")
	case runnerdomain.SessionStatusPending, runnerdomain.SessionStatusClaimed, runnerdomain.SessionStatusRunning:
		return nil, errors.New("session is already live")
	}
	if s.r.deps.Controller == nil {
		return nil, errors.New("controller not available")
	}
	if err := s.r.deps.Controller.Recover(ctx, sessionID, p.RoleKey); err != nil {
		return nil, fmt.Errorf("recover: %w", err)
	}
	msg := fmt.Sprintf("recovered by agent %s", p.RoleKey)
	_, _ = s.r.deps.Runner.AppendMessage(ctx, &runnerdomain.Message{
		SessionID: sessionID, Kind: runnerdomain.MessageKindSystem, Content: msg,
	})
	return map[string]any{
		"session_id": sessionID, "status": string(runnerdomain.SessionStatusPending), "recovered": true,
	}, nil
}

// ---- Attachments ----

// UploadAttachment uploads a file as an issue attachment.
func (s *APIService) UploadAttachment(ctx context.Context, p *apidomain.Actor, fileBytes []byte, name, displayName string, inline bool, commentID int64) (any, error) {
	_, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(fileBytes) == 0 || name == "" {
		return nil, errors.New("file and name are required")
	}
	if displayName == "" {
		displayName = name
	}
	att, err := s.r.deps.Attachments.Upload(ctx, &attachmentdomain.AttachmentUploadParams{
		Data: fileBytes, Name: name, DisplayName: displayName, Inline: inline, CommentID: commentID, AgentRole: p.RoleKey,
	})
	if err != nil {
		return nil, fmt.Errorf("upload attachment: %w", err)
	}
	url := fmt.Sprintf("/api/attachments/%d/download", att.ID)
	return map[string]any{
		"attachment_id":    att.ID,
		"display_name":     displayName,
		"original_name":    att.OriginalName,
		"size_bytes":       att.SizeBytes,
		"mime_type":        att.DetectedMimeType,
		"kind":             string(att.Kind),
		"url":              url,
		"markdown_snippet": attachmentMarkdownSnippet(att.ID, att.DisplayName, att.OriginalName, att.Kind, inline),
	}, nil
}

// ---- Releases ----

// CreateRelease creates a draft release.
func (s *APIService) CreateRelease(ctx context.Context, p *apidomain.Actor, tagName, title, notes string) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.r.deps.Releases == nil {
		return nil, errors.New("release store not available")
	}
	tagName = strings.TrimSpace(tagName)
	if tagName == "" {
		return nil, errors.New("tag_name is required")
	}
	if title == "" {
		title = tagName
	}
	sha, err := s.r.deps.Git.ResolveCommit(scope.fsPath, "refs/tags/"+tagName)
	if err != nil || sha == "" {
		return nil, errors.New("tag not found: " + tagName)
	}
	rel, err := s.r.deps.Releases.Create(ctx, scope.repo.ID, tagName, sha, title, notes, actorRef(p.RoleKey))
	if err != nil {
		return nil, fmt.Errorf("create release: %w", err)
	}
	return &apiReleaseItem{
		ID: rel.ID, TagName: rel.TagName, TargetCommitSHA: rel.TargetCommitSHA,
		Title: rel.Title, IsDraft: rel.IsDraft, CreatedAt: stableTime(rel.CreatedAt),
	}, nil
}

// UpdateRelease updates a release's metadata.
func (s *APIService) UpdateRelease(ctx context.Context, p *apidomain.Actor, id int64, tagName, title, notes *string) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.r.deps.Releases == nil {
		return nil, errors.New("release store not available")
	}
	rel, err := s.r.deps.Releases.GetByID(ctx, id)
	if err != nil {
		return nil, errors.New("release not found")
	}
	if rel.RepoID != scope.repo.ID {
		return nil, errors.New("release not in this repo")
	}
	newTagName := rel.TagName
	newTargetSHA := rel.TargetCommitSHA
	if tagName != nil && *tagName != "" && *tagName != rel.TagName {
		if !rel.IsDraft {
			return nil, errors.New("cannot change tag_name of a published release")
		}
		sha, err := s.r.deps.Git.ResolveCommit(scope.fsPath, "refs/tags/"+*tagName)
		if err != nil || sha == "" {
			return nil, errors.New("tag not found: " + *tagName)
		}
		newTagName = *tagName
		newTargetSHA = sha
	}
	newTitle := rel.Title
	if title != nil && *title != "" {
		newTitle = *title
	}
	newNotes := rel.Notes
	if notes != nil && *notes != "" {
		newNotes = *notes
	}
	updated, err := s.r.deps.Releases.Update(ctx, id, newTagName, newTargetSHA, newTitle, newNotes)
	if err != nil {
		return nil, fmt.Errorf("update release: %w", err)
	}
	return &apiReleaseItem{
		ID: updated.ID, TagName: updated.TagName, TargetCommitSHA: updated.TargetCommitSHA,
		Title: updated.Title, IsDraft: updated.IsDraft, UpdatedAt: stableTime(updated.UpdatedAt),
	}, nil
}

// DeleteRelease deletes a release and its assets.
func (s *APIService) DeleteRelease(ctx context.Context, p *apidomain.Actor, id int64) error {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return err
	}
	if s.r.deps.Releases == nil {
		return errors.New("release store not available")
	}
	rel, err := s.r.deps.Releases.GetByID(ctx, id)
	if err != nil {
		return errors.New("release not found")
	}
	if rel.RepoID != scope.repo.ID {
		return errors.New("release not in this repo")
	}
	if s.r.deps.ReleaseAssets != nil {
		assets, _ := s.r.deps.ReleaseAssets.ListByRelease(ctx, id)
		for _, a := range assets {
			if s.r.deps.AssetStorage != nil {
				_ = s.r.deps.AssetStorage.Remove(a.StorageKey)
			}
			_ = s.r.deps.ReleaseAssets.Delete(ctx, a.ID)
		}
	}
	if err := s.r.deps.Releases.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete release: %w", err)
	}
	return nil
}

// PublishRelease publishes a draft release.
func (s *APIService) PublishRelease(ctx context.Context, p *apidomain.Actor, id int64) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.r.deps.Releases == nil {
		return nil, errors.New("release store not available")
	}
	rel, err := s.r.deps.Releases.GetByID(ctx, id)
	if err != nil {
		return nil, errors.New("release not found")
	}
	if rel.RepoID != scope.repo.ID {
		return nil, errors.New("release not in this repo")
	}
	pub, err := s.r.deps.Releases.Publish(ctx, id, actorRef(p.RoleKey))
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	return &apiReleaseItem{
		ID: pub.ID, TagName: pub.TagName, IsDraft: pub.IsDraft, PublishedAt: stableTime(pub.PublishedAt),
	}, nil
}

// UploadReleaseAsset uploads a base64-encoded asset to a release.
func (s *APIService) UploadReleaseAsset(ctx context.Context, p *apidomain.Actor, releaseID int64, name, contentB64, contentType string) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.r.deps.Releases == nil || s.r.deps.ReleaseAssets == nil || s.r.deps.AssetStorage == nil {
		return nil, errors.New("release store not available")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if contentB64 == "" {
		return nil, errors.New("content is required")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	rel, err := s.r.deps.Releases.GetByID(ctx, releaseID)
	if err != nil {
		return nil, errors.New("release not found")
	}
	if rel.RepoID != scope.repo.ID {
		return nil, errors.New("release not in this repo")
	}
	decoded, err := decodeBase64(contentB64)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 content: %w", err)
	}
	storageKey := fmt.Sprintf("%d/%s", releaseID, name)
	sizeBytes, err := s.r.deps.AssetStorage.Store(storageKey, bytes.NewReader(decoded))
	if err != nil {
		return nil, fmt.Errorf("store asset: %w", err)
	}
	_, err = s.r.deps.ReleaseAssets.Create(ctx, releaseID, name, contentType, sizeBytes, storageKey, actorRef(p.RoleKey))
	if err != nil {
		_ = s.r.deps.AssetStorage.Remove(storageKey)
		return nil, fmt.Errorf("create asset: %w", err)
	}
	return map[string]any{
		"ok": true, "release_id": releaseID, "name": name, "size_bytes": sizeBytes, "content_type": contentType,
	}, nil
}

// ---- DTO mappers ----

func commentsToAPIDTO(in []*issuedomain.Comment) []apiComment {
	out := make([]apiComment, 0, len(in))
	for _, c := range in {
		author := c.AuthorName
		if c.AgentRole != "" {
			author = c.AgentRole
		}
		out = append(out, apiComment{
			ID:        c.ID,
			Author:    author,
			AgentRole: c.AgentRole,
			Body:      truncateBody(c.Body, 140),
			FilePath:  c.FilePath,
			Line:      c.Line,
			CreatedAt: stableTime(c.CreatedAt),
		})
	}
	return out
}

func eventsToAPIDTO(in []*issuedomain.Event) []apiEvent {
	out := make([]apiEvent, 0, len(in))
	for _, e := range in {
		actor := e.ActorName
		if e.AgentRole != "" {
			actor = e.AgentRole
		}
		payload := json.RawMessage(e.Payload)
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		out = append(out, apiEvent{
			ID:        e.ID,
			Kind:      string(e.Kind),
			Actor:     actor,
			AgentRole: e.AgentRole,
			Payload:   payload,
			CreatedAt: stableTime(e.CreatedAt),
		})
	}
	return out
}

func todoToAPIDTO(t *issuedomain.Todo) apiTodo {
	return apiTodo{
		ID:        t.ID,
		IssueID:   t.IssueID,
		Content:   t.Content,
		Status:    string(t.Status),
		Position:  t.Position,
		CreatedAt: stableTime(t.CreatedAt),
		UpdatedAt: stableTime(t.UpdatedAt),
	}
}

func todosToAPIDTO(todos []*issuedomain.Todo) []apiTodo {
	out := make([]apiTodo, 0, len(todos))
	for _, t := range todos {
		out = append(out, todoToAPIDTO(t))
	}
	return out
}

func todoSummaryToAPIDTO(s *issuedomain.TodoSummary) *apiTodoSum {
	if s == nil {
		return &apiTodoSum{}
	}
	return &apiTodoSum{
		Total: s.Total, Todo: s.Todo, InProgress: s.InProgress, Done: s.Done, AllDone: s.CompletedAll(),
	}
}

func contributionSummaryToAPI(c *issuedomain.Contribution) apiContribItem {
	item := apiContribItem{
		ID:        c.ID,
		IssueID:   c.IssueID,
		AgentRole: c.AgentRole,
		Actor: map[string]any{
			"kind":         string(c.Actor.Kind),
			"id":           c.Actor.ID,
			"display_name": c.Actor.DisplayName,
			"role_key":     c.Actor.RoleKey,
		},
		RefName:      c.RefName,
		HeadSHA:      c.HeadSHA,
		BaseSHA:      c.BaseSHA,
		Title:        c.Title,
		Description:  c.Description,
		Status:       string(c.Status),
		Mergeable:    c.Mergeable,
		MergeMode:    c.MergeMode,
		ChangedPaths: c.ChangedPaths,
		Files:        c.Files,
		Additions:    c.Additions,
		Deletions:    c.Deletions,
		CreatedAt:    stableTime(c.CreatedAt),
		UpdatedAt:    stableTime(c.UpdatedAt),
	}
	if c.MergedCommitSHA != "" {
		item.MergedCommitSHA = c.MergedCommitSHA
	}
	if c.MergedAt != nil {
		item.MergedAt = stableTime(*c.MergedAt)
	}
	return item
}


// ---- Questionnaires ---- //

// CreateQuestionnaire creates a new questionnaire on the current issue.
func (s *APIService) CreateQuestionnaire(ctx context.Context, p *apidomain.Actor, input apidomain.CreateQuestionnaireInput) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.r.deps.Questionnaires == nil {
		return nil, errors.New("questionnaire service not available")
	}

	params := questionnairedomain.CreateParams{
		IssueID:        scope.issue.ID,
		Title:          input.Title,
		Description:    input.Description,
		CreatedByAgent: p.RoleKey,
	}
	for i, q := range input.Questions {
		cq := questionnairedomain.CreateQuestion{
			Position: i,
			Text:     q.Text,
			Type:     questionnairedomain.Qtype(q.Type),
			Required: q.Required,
		}
		for _, o := range q.Options {
			cq.Options = append(cq.Options, questionnairedomain.Option{Label: o.Label})
		}
		params.Questions = append(params.Questions, cq)
	}

	qn, err := s.r.deps.Questionnaires.Create(ctx, params)
	if err != nil {
		return nil, err
	}

	// Write a timeline event so the frontend's existing comments+events
	// merge picks up the questionnaire and renders it as an inline card
	// at its correct chronological position.
	payload, _ := json.Marshal(issuedomain.QuestionnairePostedPayload{
		QuestionnaireID: qn.ID,
		Title:           qn.Title,
		QuestionCount:   len(qn.Questions),
	})
	_, _ = s.r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID,
		issuedomain.EventQuestionnairePosted, payload, p.RoleKey)

	return toAPIQuestionnaire(qn), nil
}

// GetQuestionnaire returns a single questionnaire by ID.
func (s *APIService) GetQuestionnaire(ctx context.Context, p *apidomain.Actor, id int64) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.r.deps.Questionnaires == nil {
		return nil, errors.New("questionnaire service not available")
	}
	qn, err := s.r.deps.Questionnaires.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if qn.IssueID != scope.issue.ID {
		return nil, errors.New("questionnaire does not belong to the current issue")
	}
	return toAPIQuestionnaire(qn), nil
}

// GetQuestionnaireResult returns aggregated results for a questionnaire.
func (s *APIService) GetQuestionnaireResult(ctx context.Context, p *apidomain.Actor, id int64) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.r.deps.Questionnaires == nil {
		return nil, errors.New("questionnaire service not available")
	}
	result, err := s.r.deps.Questionnaires.BuildResult(ctx, id)
	if err != nil {
		return nil, err
	}
	if result.Questionnaire.IssueID != scope.issue.ID {
		return nil, errors.New("questionnaire does not belong to the current issue")
	}
	return toAPIResult(result), nil
}

// ListQuestionnaires returns all questionnaires for the current issue.
func (s *APIService) ListQuestionnaires(ctx context.Context, p *apidomain.Actor) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.r.deps.Questionnaires == nil {
		return nil, errors.New("questionnaire service not available")
	}
	qns, err := s.r.deps.Questionnaires.GetByIssue(ctx, scope.issue.ID)
	if err != nil {
		return nil, err
	}
	var items []any
	for _, qn := range qns {
		items = append(items, toAPIQuestionnaire(qn))
	}
	if items == nil {
		items = []any{}
	}
	return items, nil
}

// CloseQuestionnaire closes a questionnaire.
func (s *APIService) CloseQuestionnaire(ctx context.Context, p *apidomain.Actor, id int64, reason string) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.r.deps.Questionnaires == nil {
		return nil, errors.New("questionnaire service not available")
	}
	qn, err := s.r.deps.Questionnaires.Close(ctx, id, reason)
	if err != nil {
		return nil, err
	}
	if qn.IssueID != scope.issue.ID {
		return nil, errors.New("questionnaire does not belong to the current issue")
	}
	return toAPIQuestionnaire(qn), nil
}

// ---- DTO helpers ---- //

func toAPIQuestionnaire(qn *questionnairedomain.Questionnaire) map[string]any {
	result := map[string]any{
		"id":               qn.ID,
		"issue_id":         qn.IssueID,
		"title":            qn.Title,
		"description":      qn.Description,
		"status":           string(qn.Status),
		"created_by_agent": qn.CreatedByAgent,
		"created_at":       stableTime(qn.CreatedAt),
	}
	if qn.ClosedAt != nil {
		result["closed_at"] = stableTime(*qn.ClosedAt)
	}
	if qn.ClosedReason != "" {
		result["closed_reason"] = qn.ClosedReason
	}
	var questions []map[string]any
	for _, q := range qn.Questions {
		qi := map[string]any{
			"id":       q.ID,
			"position": q.Position,
			"type":     string(q.Type),
			"text":     q.Text,
			"required": q.Required,
		}
		if q.Options != nil {
			var opts []map[string]any
			for _, o := range q.Options {
				opts = append(opts, map[string]any{"id": o.ID, "label": o.Label})
			}
			qi["options"] = opts
		}
		questions = append(questions, qi)
	}
	if questions == nil {
		questions = []map[string]any{}
	}
	result["questions"] = questions
	return result
}

func toAPIResult(r *questionnairedomain.Result) map[string]any {
	result := map[string]any{
		"submissions": r.Submissions,
	}
	if r.Questionnaire != nil {
		result["questionnaire"] = toAPIQuestionnaire(r.Questionnaire)
	}
	byQ := make(map[string]any)
	for qid, qr := range r.ByQuestion {
		qi := map[string]any{"type": string(qr.Type)}
		if qr.Tallies != nil {
			var tallies []map[string]any
			for _, t := range qr.Tallies {
				tallies = append(tallies, map[string]any{
					"option_id": t.OptionID, "label": t.Label,
					"count": t.Count, "percent": t.Percent,
				})
			}
			qi["tallies"] = tallies
		}
		if qr.Responses != nil {
			var responses []map[string]any
			for _, tr := range qr.Responses {
				responses = append(responses, map[string]any{
					"user_id":      tr.UserID,
					"user_display": tr.DisplayName,
					"text":         tr.Text,
					"submitted_at": stableTime(tr.SubmittedAt),
				})
			}
			qi["responses"] = responses
		}
		byQ[strconv.FormatInt(qid, 10)] = qi
	}
	result["by_question"] = byQ

	var submitters []map[string]any
	for _, sd := range r.Submitters {
		sm := map[string]any{
			"user_id":      sd.UserID,
			"user_display": sd.DisplayName,
			"submitted_at": stableTime(sd.SubmittedAt),
		}
		var answers []map[string]any
		for _, a := range sd.Answers {
			am := map[string]any{"question_id": a.QuestionID}
			if len(a.OptionIDs) > 0 {
				am["option_ids"] = a.OptionIDs
			}
			if a.Text != "" {
				am["text"] = a.Text
			}
			answers = append(answers, am)
		}
		sm["answers"] = answers
		submitters = append(submitters, sm)
	}
	result["submitters"] = submitters
	return result
}

// ---- Dependencies ----

// AddDependency creates a dependency edge from the session's issue to dependsOnNumber.
func (s *APIService) AddDependency(ctx context.Context, p *apidomain.Actor, dependsOnNumber int64) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	text, err := s.r.DepsAdd(ctx, scope, dependsOnNumber)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": text}, nil
}

// RemoveDependency removes a dependency edge.
func (s *APIService) RemoveDependency(ctx context.Context, p *apidomain.Actor, dependsOnNumber int64) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	text, err := s.r.DepsRemove(ctx, scope, dependsOnNumber)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": text}, nil
}

// ReadDependencies returns the dependency info for the session's issue.
func (s *APIService) ReadDependencies(ctx context.Context, p *apidomain.Actor) (any, error) {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	text, err := s.r.DepsRead(ctx, scope)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": text}, nil
}
