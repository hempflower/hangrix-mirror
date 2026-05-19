package service

import (
	"context"
	"encoding/json"
	"errors"

	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	platformmcpdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_mcp/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// issueReadTool emits the issue metadata + comment + event timeline as
// a single JSON blob. The agent uses this to get oriented at the start
// of a turn — comment thread, recent commit_pushed events, etc.
func (r *Registry) issueReadTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_read",
		Description: "Read the current issue's metadata, comments, and timeline events.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, _ json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			comments, err := r.deps.Issues.ListComments(ctx, scope.issue.ID)
			if err != nil {
				return errorResult("list comments: " + err.Error()), nil
			}
			events, err := r.deps.Issues.ListEvents(ctx, scope.issue.ID)
			if err != nil {
				return errorResult("list events: " + err.Error()), nil
			}
			out := struct {
				Number    int64           `json:"number"`
				Title     string          `json:"title"`
				Body      string          `json:"body"`
				State     string          `json:"state"`
				Base      string          `json:"base_branch"`
				Branch    string          `json:"branch_name"`
				HeadSHA   string          `json:"head_sha"`
				Author    string          `json:"author_username"`
				CreatedAt string          `json:"created_at"`
				Comments  []commentDTO    `json:"comments"`
				Events    []eventDTO      `json:"events"`
			}{
				Number:    scope.issue.Number,
				Title:     scope.issue.Title,
				Body:      scope.issue.Body,
				State:     string(scope.issue.State),
				Base:      scope.issue.BaseBranch,
				Branch:    scope.issue.BranchName,
				HeadSHA:   scope.issue.HeadSHA,
				Author:    scope.issue.AuthorName,
				CreatedAt: stableTime(scope.issue.CreatedAt),
				Comments:  commentsToDTO(comments),
				Events:    eventsToDTO(events),
			}
			return textResult(out), nil
		},
	}
}

// issueDiffTool returns the file-level unified diff between the issue
// branch and its base (using merge-base diff, equivalent to
// `git diff base...branch`). Empty list when the issue has no commits yet —
// matching the web UI's behaviour.
func (r *Registry) issueDiffTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_diff",
		Description: "Return the diff between the issue branch and its base branch (file-level unified diff).",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, _ json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			if scope.issue.HeadSHA == "" {
				return textResult(map[string]any{"files": []any{}}), nil
			}
			diffs, err := r.deps.Git.DiffMergeBase(scope.fsPath, scope.issue.BaseBranch, scope.issue.BranchName)
			if err != nil {
				if errors.Is(err, gitdomain.ErrRefNotFound) {
					return textResult(map[string]any{"files": []any{}}), nil
				}
				return errorResult("diff: " + err.Error()), nil
			}
			return textResult(map[string]any{"files": diffs}), nil
		},
	}
}

// issueChildrenTool lists sub-issues whose parent is the current issue.
// Returns a small array; merge_subissue flows in M4 use this.
func (r *Registry) issueChildrenTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_children",
		Description: "List sub-issues (child issues) of the current issue.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, _ json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			kids, err := r.deps.Issues.ListChildren(ctx, scope.issue.ID)
			if err != nil {
				return errorResult("list children: " + err.Error()), nil
			}
			items := make([]map[string]any, 0, len(kids))
			for _, k := range kids {
				items = append(items, map[string]any{
					"number": k.Number,
					"title":  k.Title,
					"state":  string(k.State),
				})
			}
			return textResult(map[string]any{"items": items}), nil
		},
	}
}

// issueChecksTool returns CI / check state. The CI module lands in a
// later milestone; today this returns an empty list with a stable schema
// so the maintainer role can ship its merge gate now and have it
// auto-populate when checks come online.
func (r *Registry) issueChecksTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_checks",
		Description: "List the latest state of each CI check on the issue's head commit. Currently always returns [].",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Call: func(_ context.Context, _ *runnerdomain.AgentSession, _ json.RawMessage) (platformmcpdomain.Result, error) {
			return textResult(map[string]any{"checks": []any{}}), nil
		},
	}
}

// rosterListTool lists every active role session on the current issue.
// Dispatcher uses this to know who's already on the call.
func (r *Registry) rosterListTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "roster_list",
		Description: "List the roles currently active on this issue.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, _ json.RawMessage) (platformmcpdomain.Result, error) {
			if sess.RepoID == nil || sess.IssueNumber == nil {
				return errorResult("session has no (repo, issue) scope"), nil
			}
			rows, err := r.deps.Runner.ListSessionsByIssue(ctx, *sess.RepoID, *sess.IssueNumber)
			if err != nil {
				return errorResult("list sessions: " + err.Error()), nil
			}
			items := make([]map[string]any, 0, len(rows))
			for _, s := range rows {
				items = append(items, map[string]any{
					"role_key":   s.RoleKey,
					"status":     string(s.Status),
					"repo_sha":   s.RepoSHA,
					"created_at": stableTime(s.CreatedAt),
				})
			}
			return textResult(map[string]any{"items": items}), nil
		},
	}
}

// ---- DTOs ----

type commentDTO struct {
	ID         int64  `json:"id"`
	Author     string `json:"author"`
	AgentRole  string `json:"agent_role,omitempty"`
	Body       string `json:"body"`
	FilePath   string `json:"file_path,omitempty"`
	Line       int    `json:"line,omitempty"`
	CreatedAt  string `json:"created_at"`
}

func commentsToDTO(in []*issuedomain.Comment) []commentDTO {
	out := make([]commentDTO, 0, len(in))
	for _, c := range in {
		author := c.AuthorName
		if c.AgentRole != "" {
			author = c.AgentRole
		}
		out = append(out, commentDTO{
			ID:        c.ID,
			Author:    author,
			AgentRole: c.AgentRole,
			Body:      c.Body,
			FilePath:  c.FilePath,
			Line:      c.Line,
			CreatedAt: stableTime(c.CreatedAt),
		})
	}
	return out
}

type eventDTO struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor,omitempty"`
	AgentRole string          `json:"agent_role,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

func eventsToDTO(in []*issuedomain.Event) []eventDTO {
	out := make([]eventDTO, 0, len(in))
	for _, e := range in {
		actor := e.ActorName
		if e.AgentRole != "" {
			actor = e.AgentRole
		}
		payload := json.RawMessage(e.Payload)
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		out = append(out, eventDTO{
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
