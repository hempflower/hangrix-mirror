// Package service implements the platform business logic backing the
// v1 REST API. The Registry owns the cross-module dependency bag and the
// shared helpers (scope loading, mergeability/review gates, contribution
// lifecycle, attachment upload, …); APIService (api.go) is the thin v1
// surface that delegates to them.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	attachmentdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	releasedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/release/domain"
	releaseinfra "github.com/hangrix/hangrix/apps/hangrix/internal/modules/release/infra"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// Registry is the platform business-logic core backing the v1 REST API.
// It owns the cross-module dependency bag and the shared helpers
// (scope loading, mergeability/review gates, attachment upload, …) that
// the v1 service delegates to. It is constructed once at startup and
// shared by the APIService.
type Registry struct {
	deps *RegistryDeps
}

type RegistryDeps struct {
	Issues        issuedomain.Store
	Contributions issuedomain.ContributionStore
	Repos         repodomain.Store
	Storage       repodomain.PathResolver
	Git           gitdomain.Git
	Runner        runnerdomain.Repo
	Spawner       agentsessiondomain.Spawner
	Archiver      agentsessiondomain.Archiver
	Controller    agentsessiondomain.Controller
	Protections   repodomain.ProtectionStore
	Guards        []repodomain.BranchWriteGuard
	Releases      releasedomain.Store
	ReleaseAssets releasedomain.AssetStore
	AssetStorage  *releaseinfra.AssetStorage
	Attachments   attachmentdomain.Uploader
	Todos         issuedomain.TodoStore
}

// NewRegistry constructs the business-logic core, capturing the shared
// cross-module dependency bag the v1 service delegates to.
func NewRegistry(deps *RegistryDeps) *Registry {
	return &Registry{deps: deps}
}

// ---- helpers ----

// resolveRepoForSession centralises the (session → repo, fsPath, issue)
// lookup every tool needs. Tools without a host repo / issue context
// (admin smoke sessions) get a tagged "no scope" error so the LLM sees
// a clear message instead of a panic.
type sessionScope struct {
	repo   *repodomain.Repo
	fsPath string
	issue  *issuedomain.Issue
}

func (r *Registry) loadScope(ctx context.Context, sess *runnerdomain.AgentSession) (*sessionScope, error) {
	if sess == nil {
		return nil, errors.New("no session in context")
	}
	if sess.RepoID == nil || sess.IssueNumber == nil {
		return nil, errors.New("session has no (repo, issue) scope — tool unavailable")
	}
	repo, err := r.deps.Repos.GetByID(ctx, *sess.RepoID)
	if err != nil {
		return nil, fmt.Errorf("load repo: %w", err)
	}
	fsPath, err := r.deps.Storage.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		return nil, fmt.Errorf("resolve repo path: %w", err)
	}
	iss, err := r.deps.Issues.GetByNumber(ctx, repo.ID, int64(*sess.IssueNumber))
	if err != nil {
		return nil, fmt.Errorf("load issue: %w", err)
	}
	return &sessionScope{repo: repo, fsPath: fsPath, issue: iss}, nil
}

func textResult(v any) apidomain.Result {
	body, err := json.Marshal(v)
	if err != nil {
		return apidomain.Result{
			Text:    fmt.Sprintf(`{"error":"marshal: %s"}`, err),
			IsError: true,
		}
	}
	return apidomain.Result{Text: string(body)}
}

func errorResult(msg string) apidomain.Result {
	return apidomain.Result{Text: msg, IsError: true}
}

// stableTime serialises a time.Time as RFC3339 so JSON output is
// deterministic across runs. Used by the read tools.
func stableTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// fanCommentMentions is used by the issue_comment tool to fire a
// follow-on `issue.comment` trigger. The comment we just inserted may
// itself contain `@agent-<role>` tokens; per-role CommentFilter (e.g.
// mentioned_only) lives on the host yaml, so we just hand the spawner
// the full CommentContext and let it route.
func (r *Registry) fanCommentMentions(ctx context.Context, sess *runnerdomain.AgentSession, scope *sessionScope, c *issuedomain.Comment) {
	if r.deps.Spawner == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"comment_id":   c.ID,
		"comment_body": c.Body,
		"agent_role":   c.AgentRole,
		"author_id":    c.AuthorID,
		"author_name":  c.AuthorName,
		"file_path":    c.FilePath,
		"line":         c.Line,
	})
	commentCtx := &agentsessiondomain.CommentContext{
		AuthorRoleKey: c.AgentRole,
		Mentions:      agentsconfig.ParseMentions(c.Body),
	}
	if c.AgentRole == "" {
		commentCtx.AuthorUser = c.AuthorName
	}
	_, _ = r.deps.Spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
		Trigger:     agentsconfig.TriggerIssueComment,
		CauseKind:   agentsessiondomain.CauseKindCommentMentioned,
		CauseID:     strconv.FormatInt(c.ID, 10),
		RepoID:      scope.repo.ID,
		IssueNumber: *sess.IssueNumber,
		ActorID:     sess.CreatedBy,
		Comment:     commentCtx,
		Payload:     payload,
	})
}
