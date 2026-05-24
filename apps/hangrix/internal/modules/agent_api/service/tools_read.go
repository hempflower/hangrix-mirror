package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentapidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_api/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// issueReadTool emits the issue metadata + comment + event timeline as
// a single JSON blob. The agent uses this to get oriented at the start
// of a turn — comment thread, recent commit_pushed events, etc.
func (r *Registry) issueReadTool() *agentapidomain.Tool {
	return &agentapidomain.Tool{
		Name:        "issue_read",
		Description: "Read the current issue's metadata, comments, and timeline events.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, _ json.RawMessage) (agentapidomain.Result, error) {
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
				Number    int64        `json:"number"`
				Title     string       `json:"title"`
				Body      string       `json:"body"`
				State     string       `json:"state"`
				Base      string       `json:"base_branch"`
				Branch    string       `json:"branch_name"`
				HeadSHA   string       `json:"head_sha"`
				Author    string       `json:"author_username"`
				CreatedAt string       `json:"created_at"`
				Comments  []commentDTO `json:"comments"`
				Events    []eventDTO   `json:"events"`
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

// issueReadByNumberTool reads any issue in the same repository by its
// number (e.g. #91). This fills the gap where an agent creates a child
// issue and later needs to read its full state without switching sessions.
// Scope is limited to the calling session's repo — cross-repo lookups
// return a unified "not found / out of scope" soft error.
func (r *Registry) issueReadByNumberTool() *agentapidomain.Tool {
	return &agentapidomain.Tool{
		Name:        "issue_read_by_number",
		Description: "Read an issue's metadata, comments, and timeline events by its number (e.g. 91). Only issues in the same repository as the current session are accessible.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"issue_number": map[string]any{
					"type":        "integer",
					"description": "Issue number to read (e.g. 91).",
				},
			},
			"required": []any{"issue_number"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (agentapidomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}

			var req struct {
				IssueNumber int64 `json:"issue_number"`
			}
			if err := json.Unmarshal(args, &req); err != nil {
				return errorResult("invalid args: " + err.Error()), nil
			}
			if req.IssueNumber <= 0 {
				return errorResult("issue_number must be a positive integer"), nil
			}

			iss, err := r.deps.Issues.GetByNumber(ctx, scope.repo.ID, req.IssueNumber)
			if err != nil {
				if errors.Is(err, issuedomain.ErrIssueNotFound) {
					return errorResult("issue not found or out of scope"), nil
				}
				return errorResult("load issue: " + err.Error()), nil
			}

			comments, err := r.deps.Issues.ListComments(ctx, iss.ID)
			if err != nil {
				return errorResult("list comments: " + err.Error()), nil
			}
			events, err := r.deps.Issues.ListEvents(ctx, iss.ID)
			if err != nil {
				return errorResult("list events: " + err.Error()), nil
			}
			out := struct {
				Number       int64        `json:"number"`
				Title        string       `json:"title"`
				Body         string       `json:"body"`
				State        string       `json:"state"`
				Base         string       `json:"base_branch"`
				Branch       string       `json:"branch_name"`
				HeadSHA      string       `json:"head_sha"`
				Author       string       `json:"author_username"`
				ParentNumber int64        `json:"parent_number"`
				CreatedAt    string       `json:"created_at"`
				Comments     []commentDTO `json:"comments"`
				Events       []eventDTO   `json:"events"`
			}{
				Number:       iss.Number,
				Title:        iss.Title,
				Body:         iss.Body,
				State:        string(iss.State),
				Base:         iss.BaseBranch,
				Branch:       iss.BranchName,
				HeadSHA:      iss.HeadSHA,
				Author:       iss.AuthorName,
				ParentNumber: iss.ParentNumber,
				CreatedAt:    stableTime(iss.CreatedAt),
				Comments:     commentsToDTO(comments),
				Events:       eventsToDTO(events),
			}
			return textResult(out), nil
		},
	}
}


// issueCommentReadTool reads a single comment by its id. Only comments on the
// current session's issue are accessible — cross-issue lookups return a
// "not found" soft error. The body is returned in full (no truncation),
// unlike the summaries emitted by issue_read / issue_read_by_number.
func (r *Registry) issueCommentReadTool() *agentapidomain.Tool {
	return &agentapidomain.Tool{
		Name:        "issue_comment_read",
		Description: "Read a single comment by its id. Only comments on the current session's issue are accessible — cross-issue lookups return 'not found'. Returns the full body (no truncation).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"comment_id": map[string]any{
					"type":        "integer",
					"description": "The comment id to read (required). Must belong to the current session's issue.",
				},
			},
			"required": []any{"comment_id"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (agentapidomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}

			var req struct {
				CommentID int64 `json:"comment_id"`
			}
			if err := json.Unmarshal(args, &req); err != nil {
				return errorResult("invalid args: " + err.Error()), nil
			}
			if req.CommentID <= 0 {
				return errorResult("comment_id must be a positive integer"), nil
			}

			c, err := r.deps.Issues.GetCommentByID(ctx, req.CommentID)
			if err != nil {
				return errorResult("comment not found"), nil
			}
			if c.IssueID != scope.issue.ID {
				return errorResult("comment not found or out of scope"), nil
			}

			author := c.AuthorName
			if c.AgentRole != "" {
				author = c.AgentRole
			}
			dto := commentDTO{
				ID:        c.ID,
				Author:    author,
				AgentRole: c.AgentRole,
				Body:      c.Body,
				FilePath:  c.FilePath,
				Line:      c.Line,
				CreatedAt: stableTime(c.CreatedAt),
			}
			return textResult(dto), nil
		},
	}
}

// issueChildrenTool lists sub-issues whose parent is the current issue.
// Returns a small array; merge_subissue flows in M4 use this.
func (r *Registry) issueChildrenTool() *agentapidomain.Tool {
	return &agentapidomain.Tool{
		Name:        "issue_children",
		Description: "List sub-issues (child issues) of the current issue.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, _ json.RawMessage) (agentapidomain.Result, error) {
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
					"id":     k.ID,
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
func (r *Registry) issueChecksTool() *agentapidomain.Tool {
	return &agentapidomain.Tool{
		Name:        "issue_checks",
		Description: "List the latest state of each CI check on the issue's head commit. Currently always returns [].",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Call: func(_ context.Context, _ *runnerdomain.AgentSession, _ json.RawMessage) (agentapidomain.Result, error) {
			return textResult(map[string]any{"checks": []any{}}), nil
		},
	}
}

// rosterListTool lists every active role session on the current issue.
// Dispatcher uses this to know who's already on the call.
func (r *Registry) rosterListTool() *agentapidomain.Tool {
	return &agentapidomain.Tool{
		Name:        "roster_list",
		Description: "List every active role session on the current issue.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, _ json.RawMessage) (agentapidomain.Result, error) {
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
					"role_key":         s.RoleKey,
					"status":           string(s.Status),
					"repo_sha":         s.RepoSHA,
					"created_at":       stableTime(s.CreatedAt),
					"last_activity_at": stableTime(lastActivityAt(s)),
				})
			}
			return textResult(map[string]any{"items": items}), nil
		},
	}
}

// lastActivityAt returns the most recent activity timestamp for the session
// by taking the maximum of container_last_used_at, ended_at, started_at,
// claimed_at, and created_at.
func lastActivityAt(s *runnerdomain.AgentSession) time.Time {
	latest := s.CreatedAt
	if s.ClaimedAt != nil && s.ClaimedAt.After(latest) {
		latest = *s.ClaimedAt
	}
	if s.StartedAt != nil && s.StartedAt.After(latest) {
		latest = *s.StartedAt
	}
	if s.EndedAt != nil && s.EndedAt.After(latest) {
		latest = *s.EndedAt
	}
	if s.ContainerLastUsedAt != nil && s.ContainerLastUsedAt.After(latest) {
		latest = *s.ContainerLastUsedAt
	}
	return latest
}

// ---- DTOs ----

// truncateSuffix is appended by truncateBody when the input exceeds
// maxRunes. Its length is reserved from the maxRunes budget so the
// returned string never overflows the cap.
const truncateSuffix = "… (truncated)"

// truncateBody returns s unchanged when it fits within maxRunes Unicode
// characters (runes). Longer strings are shortened so that the returned
// text — including the "… (truncated)" suffix — does not exceed maxRunes.
func truncateBody(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	suffixRunes := []rune(truncateSuffix)
	budget := maxRunes - len(suffixRunes)
	if budget < 0 {
		return string(runes[:maxRunes])
	}
	return string(runes[:budget]) + truncateSuffix
}
type commentDTO struct {
	ID        int64  `json:"id"`
	Author    string `json:"author"`
	AgentRole string `json:"agent_role,omitempty"`
	Body      string `json:"body"`
	FilePath  string `json:"file_path,omitempty"`
	Line      int    `json:"line,omitempty"`
	CreatedAt string `json:"created_at"`
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
			Body:      truncateBody(c.Body, 140),
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

type mergeableResult struct {
	Mergeable  bool   `json:"mergeable"`
	Mode       string `json:"mode"`
	BaseBranch string `json:"base_branch"`
	BaseSHA    string `json:"base_sha"`
	HeadSHA    string `json:"head_sha"`
	Hint       string `json:"hint"`
}

// issueMergeableTool returns mergeable status for the current issue's branch
// vs its base. A no-parameter read-only tool — the scope is determined from
// the session's repo+issue. Agents call this before issue_merge to avoid a
// failed merge round-trip.
func (r *Registry) issueMergeableTool() *agentapidomain.Tool {
	return &agentapidomain.Tool{
		Name:        "issue_mergeable",
		Description: "Check whether the issue branch can be merged into its base — tries fast-forward first, then checks whether a merge commit would succeed. mergeable=true means issue_merge is expected to succeed. Returns mergeable, mode, base_branch, base_sha, head_sha, and hint.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, _ json.RawMessage) (agentapidomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			iss := scope.issue
			if iss.HeadSHA == "" {
				return textResult(mergeableResult{
					Mergeable:  false,
					Mode:       "unknown",
					BaseBranch: iss.BaseBranch,
					Hint:       "issue branch has no commits yet",
				}), nil
			}
			baseSHA, err := r.deps.Git.ResolveCommit(scope.fsPath, iss.BaseBranch)
			if err != nil {
				if errors.Is(err, gitdomain.ErrRefNotFound) {
					return textResult(mergeableResult{
						Mergeable:  false,
						Mode:       "unknown",
						BaseBranch: iss.BaseBranch,
						HeadSHA:    iss.HeadSHA,
						Hint:       "base branch not found",
					}), nil
				}
				return errorResult("resolve base: " + err.Error()), nil
			}
			mergeable, mode, hint, err := r.deps.Git.CheckAutoMerge(scope.fsPath, iss.BaseBranch, iss.HeadSHA)
			if err != nil {
				return errorResult("check auto-merge: " + err.Error()), nil
			}
			// When the issue branch conflicts with its base, rewrite the hint
			// to guide the agent toward resolving via a new contribution branch
			// rather than pushing the issue branch directly.
			if mode == "conflicted" {
				hint = fmt.Sprintf(
					"merge would conflict with `%s` — create a new contribution branch from the latest `issue/%d`, resolve the conflict there, push it to `refs/heads/issue-%d/%s/<slug>`, then land it via the contribution review/apply flow; do not push the issue branch directly",
					iss.BaseBranch, iss.Number, iss.Number, sess.RoleKey,
				)
			}
			// Second-level (issue → base) gate: even when git says the branch
			// is mergeable, block while contributions are still open or the
			// issue branch carries no changes (nothing applied into it yet).
			if block := r.issueMergeBlock(ctx, scope); block != "" {
				mergeable = false
				mode = "blocked"
				hint = block
			}
			result := mergeableResult{
				Mergeable:  mergeable,
				Mode:       mode,
				BaseBranch: iss.BaseBranch,
				BaseSHA:    baseSHA,
				HeadSHA:    iss.HeadSHA,
				Hint:       hint,
			}
			return textResult(result), nil
		},
	}
}



