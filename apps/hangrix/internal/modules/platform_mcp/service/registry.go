package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	platformmcpdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_mcp/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// Registry is the platform-tool catalogue. It exposes every tool a
// session's role may invoke via MCP. Per-role filtering happens via the
// session's role_config snapshot (see RoleCanList) — the catalogue
// itself is global.
type Registry struct {
	tools []*platformmcpdomain.Tool
	deps  *RegistryDeps
}

type RegistryDeps struct {
	Issues   issuedomain.Store
	Repos    repodomain.Store
	Storage  repodomain.PathResolver
	Git      gitdomain.Git
	Runner   runnerdomain.Repo
	Spawner  agentsessiondomain.Spawner
	Archiver agentsessiondomain.Archiver
}

// NewRegistry assembles the tool catalogue at startup. Tools share the
// same deps bag; per-tool constructors capture only what they need.
// Order matters for catalog stability: read-only first, then mutating
// tools (the LLM has a slight bias toward earlier tools in long
// catalogues, and "look before you act" is the safer default ordering).
func NewRegistry(deps *RegistryDeps) *Registry {
	r := &Registry{deps: deps}
	r.tools = []*platformmcpdomain.Tool{
		r.issueReadTool(),
		r.issueDiffTool(),
		r.issueChildrenTool(),
		r.issueChecksTool(),
		r.rosterListTool(),
		r.issueCommentTool(),
		r.issueReviewVoteTool(),
		r.issueCloseTool(),
		r.issueMergeTool(),
	}
	return r
}

// All returns the full tool catalogue. The MCP handler intersects this
// with the per-role `can:` filter before returning to the agent.
func (r *Registry) All() []*platformmcpdomain.Tool { return r.tools }

// ByName looks up a tool by its wire name. nil when unknown — callers
// surface "unknown tool" as a structured MCP error rather than crashing.
func (r *Registry) ByName(name string) *platformmcpdomain.Tool {
	for _, t := range r.tools {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// FilterForSession returns the subset of tools the session's role is
// allowed to invoke. The ACL is read off the role_config snapshot
// frozen at spawn time — host yaml changes mid-session don't affect
// a running agent. Whitelist (`can:`) wins over blacklist (`not:`)
// when both are set; an entirely empty ACL fails closed.
func (r *Registry) FilterForSession(sess *runnerdomain.AgentSession) []*platformmcpdomain.Tool {
	out := make([]*platformmcpdomain.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if CanCallTool(sess, t.Name) {
			out = append(out, t)
		}
	}
	return out
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

func textResult(v any) platformmcpdomain.Result {
	body, err := json.Marshal(v)
	if err != nil {
		return platformmcpdomain.Result{
			Text:    fmt.Sprintf(`{"error":"marshal: %s"}`, err),
			IsError: true,
		}
	}
	return platformmcpdomain.Result{Text: string(body)}
}

func errorResult(msg string) platformmcpdomain.Result {
	return platformmcpdomain.Result{Text: msg, IsError: true}
}

// stableTime serialises a time.Time as RFC3339 so JSON output is
// deterministic across runs. Used by the read tools.
func stableTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// strconvAtoi64 is a tiny convenience used by tools that parse cause
// IDs (comment IDs etc.) emitted as JSON numbers or strings. We never
// trust the LLM-emitted ID to be a specific JSON type.
func strconvAtoi64(raw any) (int64, bool) {
	switch v := raw.(type) {
	case float64:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil
	}
	return 0, false
}

// requireMentionTrigger is used by the issue_comment tool to fire a
// follow-on mention path. Strictly: the comment we just inserted may
// itself contain `@agent-<role>` tokens and we want those roles to
// wake. We re-use the spawner's mention pipeline rather than
// duplicating the logic.
func (r *Registry) fanCommentMentions(ctx context.Context, sess *runnerdomain.AgentSession, scope *sessionScope, c *issuedomain.Comment) {
	if r.deps.Spawner == nil {
		return
	}
	mentions := agentsconfig.ParseMentions(c.Body)
	if len(mentions) == 0 {
		// Still fire comment.any so a dispatcher-style listener sees it.
		r.fireCommentAny(ctx, sess, scope, c)
		return
	}
	cfg, err := r.deps.Spawner.LoadHostConfig(ctx, scope.repo.ID)
	if err != nil || cfg == nil {
		r.fireCommentAny(ctx, sess, scope, c)
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
	// comment.any first so subscribers see the comment even if no
	// mention matches.
	r.fireCommentAnyPayload(ctx, sess, scope, c, payload)
	for _, roleKey := range mentions {
		if _, ok := cfg.Roles[roleKey]; !ok {
			continue
		}
		// Mention routing is open: any commenter (human or agent) can
		// wake any role declared in the host yaml. The host yaml's
		// `can:` for `issue_comment` already governs who is allowed
		// to author the originating comment.
		_, _ = r.deps.Spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
			Trigger:     agentsconfig.TriggerIssueCommentMentioned,
			CauseKind:   agentsessiondomain.CauseKindCommentMentioned,
			CauseID:     strconv.FormatInt(c.ID, 10),
			RepoID:      scope.repo.ID,
			IssueNumber: *sess.IssueNumber,
			ActorID:     sess.CreatedBy,
			RoleKey:     roleKey,
			Payload:     payload,
		})
	}
}

func (r *Registry) fireCommentAny(ctx context.Context, sess *runnerdomain.AgentSession, scope *sessionScope, c *issuedomain.Comment) {
	payload, _ := json.Marshal(map[string]any{
		"comment_id":   c.ID,
		"comment_body": c.Body,
		"agent_role":   c.AgentRole,
		"author_id":    c.AuthorID,
		"author_name":  c.AuthorName,
	})
	r.fireCommentAnyPayload(ctx, sess, scope, c, payload)
}

func (r *Registry) fireCommentAnyPayload(ctx context.Context, sess *runnerdomain.AgentSession, scope *sessionScope, c *issuedomain.Comment, payload []byte) {
	_, _ = r.deps.Spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
		Trigger:     agentsconfig.TriggerIssueCommentAny,
		CauseKind:   agentsessiondomain.CauseKindCommentMentioned,
		CauseID:     strconv.FormatInt(c.ID, 10),
		RepoID:      scope.repo.ID,
		IssueNumber: *sess.IssueNumber,
		ActorID:     sess.CreatedBy,
		Payload:     payload,
	})
}
