package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	platformmcpdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_mcp/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// issueCommentTool posts an agent-authored comment onto the current
// issue. AuthorID is NULL on the DB side (the role isn't a user);
// agent_role carries the role key from the session's snapshot. Mentions
// inside the body fan out the same way human comments do — re-using the
// spawner pipeline so the wakeup behaviour is identical.
func (r *Registry) issueCommentTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_comment",
		Description: "Post a comment on the current issue. `body` is markdown; @agent-<role-key> mentions wake other roles.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"body": map[string]any{
					"type":        "string",
					"description": "The comment body. Markdown allowed; mentions follow @agent-<role-key> grammar.",
				},
				"file_path": map[string]any{
					"type":        "string",
					"description": "Optional path to anchor the comment to a file (inline review). Omit for top-level.",
				},
				"line": map[string]any{
					"type":        "integer",
					"description": "Optional line number to anchor inline. Requires file_path.",
				},
			},
			"required": []string{"body"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			var req struct {
				Body     string `json:"body"`
				FilePath string `json:"file_path"`
				Line     int    `json:"line"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			body := strings.TrimSpace(req.Body)
			if body == "" {
				return errorResult("body is required"), nil
			}
			if sess.RoleKey == "" {
				return errorResult("session has no role_key (admin smoke path?)"), nil
			}
			c, err := r.deps.Issues.CreateAgentComment(
				ctx, scope.issue.ID, sess.RoleKey, body, strings.TrimSpace(req.FilePath), req.Line,
			)
			if err != nil {
				return errorResult("create comment: " + err.Error()), nil
			}
			r.fanCommentMentions(ctx, sess, scope, c)
			return textResult(map[string]any{
				"id":         c.ID,
				"agent_role": c.AgentRole,
				"created_at": stableTime(c.CreatedAt),
			}), nil
		},
	}
}

// issueReviewVoteTool records a structured review vote on the issue.
// Persistence: an issue_events row of kind=review_vote with
// payload={value, reason}. Side-effect: fires the review_vote.posted
// trigger so maintainer roles wake.
func (r *Registry) issueReviewVoteTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_review_vote",
		Description: "Cast a structured review vote on the current issue (approve / request_changes / abstain).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{
					"type":        "string",
					"enum":        []string{"approve", "request_changes", "abstain"},
					"description": "Vote outcome.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Free-text rationale shown on the timeline. Recommended even for 'approve'.",
				},
			},
			"required": []string{"value"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			var req struct {
				Value  string `json:"value"`
				Reason string `json:"reason"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			value := issuedomain.ReviewVoteValue(strings.TrimSpace(req.Value))
			if !value.Valid() {
				return errorResult("value must be approve|request_changes|abstain"), nil
			}
			payload, _ := json.Marshal(issuedomain.ReviewVotePayload{
				Value:  value,
				Reason: req.Reason,
			})
			evt, err := r.deps.Issues.CreateAgentEvent(
				ctx, scope.issue.ID, issuedomain.EventReviewVote, payload, sess.RoleKey,
			)
			if err != nil {
				return errorResult("create vote event: " + err.Error()), nil
			}
			// Fan out review_vote.posted so maintainer wakes.
			if r.deps.Spawner != nil {
				triggerPayload, _ := json.Marshal(map[string]any{
					"event_id":   evt.ID,
					"role_key":   sess.RoleKey,
					"value":      string(value),
					"reason":     req.Reason,
				})
				_, _ = r.deps.Spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
					Trigger:     agentsconfig.TriggerReviewVotePosted,
					CauseKind:   agentsessiondomain.CauseKindReviewVote,
					CauseID:     fmt.Sprintf("vote-%d", evt.ID),
					RepoID:      scope.repo.ID,
					IssueNumber: *sess.IssueNumber,
					ActorID:     sess.CreatedBy,
					Payload:     triggerPayload,
				})
			}
			return textResult(map[string]any{
				"event_id": evt.ID,
				"value":    string(value),
			}), nil
		},
	}
}

// issueCloseTool transitions the current issue to state=closed and
// archives all sessions on it. Idempotent — closing an already-closed
// issue returns a "no change" result. Re-opening is intentionally not
// available to agents.
func (r *Registry) issueCloseTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_close",
		Description: "Close the current issue without merging. Archives every active agent session on it.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional rationale, recorded on the timeline.",
				},
			},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			if scope.issue.State != issuedomain.StateOpen {
				return textResult(map[string]any{
					"state":    string(scope.issue.State),
					"changed":  false,
				}), nil
			}
			var req struct {
				Reason string `json:"reason"`
			}
			_ = unmarshalArgs(args, &req)
			next, err := r.deps.Issues.UpdateState(ctx, scope.issue.ID, issuedomain.StateClosed, "")
			if err != nil {
				return errorResult("update state: " + err.Error()), nil
			}
			payload, _ := json.Marshal(issuedomain.StateChangedPayload{
				From: scope.issue.State, To: issuedomain.StateClosed,
			})
			_, _ = r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventStateChanged, payload, sess.RoleKey)
			if r.deps.Archiver != nil {
				_, _ = r.deps.Archiver.OnIssueClosed(ctx, scope.repo.ID, *sess.IssueNumber)
			}
			return textResult(map[string]any{
				"state":   string(next.State),
				"changed": true,
			}), nil
		},
	}
}

// issueMergeTool merges the issue branch into base. The work is the
// same as the web-API merge handler: three-way merge → state →
// timeline events → archive sessions.
//
// The agent path differs from the web path in one place: there's no
// canManage permission check here because the `can: [issue_merge]`
// ACL on the role is the authorization gate — only roles the operator
// explicitly grants merge get to call this tool.
func (r *Registry) issueMergeTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_merge",
		Description: "Merge the issue branch into its base. Fails if there are no commits or the merge would conflict.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "Optional merge commit message. Defaults to 'Merge issue #N: <title>'.",
				},
			},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			if scope.issue.State != issuedomain.StateOpen {
				return errorResult(fmt.Sprintf("issue is %s, not open", scope.issue.State)), nil
			}
			headSHA, err := r.deps.Git.ResolveCommit(scope.fsPath, scope.issue.BranchName)
			if err != nil || headSHA == "" {
				return errorResult("issue branch has no commits yet"), nil
			}

			var req struct {
				Message string `json:"message"`
			}
			_ = unmarshalArgs(args, &req)
			msg := strings.TrimSpace(req.Message)
			if msg == "" {
				msg = fmt.Sprintf("Merge issue #%d: %s", scope.issue.Number, scope.issue.Title)
			}

			identity := agentsessiondomain.IdentityForRole(sess.RoleKey, "")
			mergeSHA, mode, err := r.deps.Git.MergeBranch(
				scope.fsPath, scope.issue.BaseBranch, scope.issue.BranchName, msg, gitdomain.Signature{
					Name:  identity.Name,
					Email: identity.Email,
					When:  time.Now(),
				},
			)
			if err != nil {
				if errors.Is(err, gitdomain.ErrMergeConflict) {
					return errorResult("merge conflict — rebase the issue branch onto " + scope.issue.BaseBranch), nil
				}
				return errorResult("merge: " + err.Error()), nil
			}

			if _, err := r.deps.Issues.UpdateState(ctx, scope.issue.ID, issuedomain.StateMerged, mergeSHA); err != nil {
				return errorResult("update state: " + err.Error()), nil
			}

			mergePayload, _ := json.Marshal(issuedomain.BranchMergedPayload{
				IntoBranch: scope.issue.BaseBranch,
				FromBranch: scope.issue.BranchName,
				MergeSHA:   mergeSHA,
				Mode:       mode,
			})
			_, _ = r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventBranchMerged, mergePayload, sess.RoleKey)
			statePayload, _ := json.Marshal(issuedomain.StateChangedPayload{
				From: issuedomain.StateOpen, To: issuedomain.StateMerged,
			})
			_, _ = r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventStateChanged, statePayload, sess.RoleKey)

			if r.deps.Archiver != nil {
				_, _ = r.deps.Archiver.OnIssueClosed(ctx, scope.repo.ID, *sess.IssueNumber)
			}

			return textResult(map[string]any{
				"merge_sha": mergeSHA,
				"mode":      mode,
			}), nil
		},
	}
}

// unmarshalArgs accepts an empty body as the empty object — LLMs
// occasionally emit `""` for no-arg tools and we don't want that to
// reject the call.
func unmarshalArgs(raw json.RawMessage, dst any) error {
	body := []byte(raw)
	if len(body) == 0 || strings.TrimSpace(string(body)) == "" {
		body = []byte(`{}`)
	}
	return json.Unmarshal(body, dst)
}
