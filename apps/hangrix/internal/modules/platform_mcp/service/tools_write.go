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
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
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
			if agentsconfig.HasBacktickWrappedMention(body) {
				return errorResult("body contains an @agent-<role> mention wrapped in backticks — remove the backticks around the mention so the parser can see it, or omit the mention entirely"), nil
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
					"event_id": evt.ID,
					"role_key": sess.RoleKey,
					"value":    string(value),
					"reason":   req.Reason,
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
					"state":   string(scope.issue.State),
					"changed": false,
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
// same as the web-API merge handler: rebase-first merge → state →
// timeline events → archive sessions.
//
// The agent path differs from the web path in one place: there's no
// canManage permission check here because the `can: [issue_merge]`
// ACL on the role is the authorization gate — only roles the operator
// explicitly grants merge get to call this tool.
func (r *Registry) issueMergeTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_merge",
		Description: "Merge the issue branch into its base using a rebase-first strategy: fast-forward if possible, otherwise auto-rebase the issue branch onto the base tip. Fails if there are no commits or the rebase would conflict.",
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
			// Snapshot base tip before MergeBranch rewrites it; needed so
			// post-merge commit listings can recover the divergence point
			// even after a fast-forward.
			preMergeBaseSHA, _ := r.deps.Git.ResolveCommit(scope.fsPath, scope.issue.BaseBranch)

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
				BaseSHA:    preMergeBaseSHA,
				MergeSHA:   mergeSHA,
				Mode:       mode,
			})
			_, _ = r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventBranchMerged, mergePayload, sess.RoleKey)
			statePayload, _ := json.Marshal(issuedomain.StateChangedPayload{
				From: issuedomain.StateOpen, To: issuedomain.StateMerged,
			})
			_, _ = r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventStateChanged, statePayload, sess.RoleKey)

			// Try to delete the issue branch unless the host config
			// disables it. Same logic as the web merge handler.
			cleanup := r.tryDeleteIssueBranch(ctx, scope.repo.ID, scope.fsPath, scope.issue.BranchName)

			if r.deps.Archiver != nil {
				_, _ = r.deps.Archiver.OnIssueClosed(ctx, scope.repo.ID, *sess.IssueNumber)
			}

			result := map[string]any{
				"merge_sha": mergeSHA,
				"mode":      mode,
			}
			if cleanup != nil {
				result["cleanup"] = cleanup
			}
			return textResult(result), nil
		},
	}
}

// tryDeleteIssueBranch mirrors the same-named helper in the issue HTTP
// handler. It consults the host config, branch protections, guards, then
// calls git.DeleteBranch — all best-effort; failure never rolls back merge.
func (r *Registry) tryDeleteIssueBranch(ctx context.Context, repoID int64, fsPath, branchName string) *mergeCleanupResult {
	// Consult host config. Missing yaml = nil config → defaults apply.
	if r.deps.Spawner != nil {
		cfg, err := r.deps.Spawner.LoadHostConfig(ctx, repoID)
		if err == nil && cfg != nil && cfg.Issues != nil && !cfg.Issues.DeleteBranchOnMerge {
			return nil
		}
	}

	// Check branch protections.
	if r.deps.Protections != nil {
		rules, err := r.deps.Protections.List(ctx, repoID)
		if err == nil {
			if rule := repodomain.MatchProtection(rules, branchName); rule != nil && rule.ForbidDelete {
				return &mergeCleanupResult{Deleted: false, Reason: "protected"}
			}
		}
	}

	// Run branch-write guards.
	oldSHA, _ := r.deps.Git.ResolveCommit(fsPath, branchName)
	for _, g := range r.deps.Guards {
		if err := g.CheckBranchWrite(ctx, repodomain.BranchWriteOp{
			RepoID:     repoID,
			Branch:     branchName,
			OldSHA:     oldSHA,
			IsDelete:   true,
			IsInternal: true,
		}); err != nil {
			return &mergeCleanupResult{Deleted: false, Reason: "denied"}
		}
	}

	if err := r.deps.Git.DeleteBranch(fsPath, branchName); err != nil {
		return &mergeCleanupResult{Deleted: false, Reason: "delete_failed"}
	}
	return &mergeCleanupResult{Deleted: true}
}

// mergeCleanupResult duplicates mergeCleanup from the issue HTTP handler
// so the platform_mcp module stays decoupled from the issue handler package.
type mergeCleanupResult struct {
	Deleted bool   `json:"deleted"`
	Reason  string `json:"reason,omitempty"`
}

// sessionRecoverTool recovers a failed / succeeded / cancelled / idle session
// on the same issue back to pending so a runner picks it up again. Scope is
// constrained to the caller's issue; cross-issue recovery is rejected. ACL
// is driven by the host yaml `can: [session_recover]` whitelist — the handler
// checks CanCallTool before dispatch and this tool double-checks the scope.
//
// Uses Controller.Recover() (not Resume) so the target session receives a
// manual.recover event whose payload carries the caller's role key
// (recovered_by), as required by spec AC 5.
func (r *Registry) sessionRecoverTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "session_recover",
		Description: "Recover a failed / succeeded / cancelled / idle agent session on the current issue back to pending so a runner picks it up again. `session_id` is required. Only sessions on the same issue can be recovered; cross-issue and archived sessions are rejected. Requires `session_recover` in the role's `can:` whitelist.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{
					"type":        "integer",
					"description": "Target session ID to recover.",
				},
			},
			"required": []string{"session_id"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			var req struct {
				SessionID int64 `json:"session_id"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			if req.SessionID <= 0 {
				return errorResult("session_id is required and must be positive"), nil
			}

			target, err := r.deps.Runner.GetSessionByID(ctx, req.SessionID)
			if err != nil {
				return errorResult("load session: " + err.Error()), nil
			}

			// Same-issue gate: target repo and issue must match the caller.
			if target.RepoID == nil || *target.RepoID != scope.repo.ID {
				return errorResult("target session not in same repo"), nil
			}
			if target.IssueNumber == nil || *target.IssueNumber != int32(*sess.IssueNumber) {
				return errorResult("target session not in same issue"), nil
			}

			// Status gate: only terminal-or-idle rows are recoverable.
			switch target.Status {
			case runnerdomain.SessionStatusArchived:
				return errorResult("session is archived, not resumable"), nil
			case runnerdomain.SessionStatusPending,
				runnerdomain.SessionStatusClaimed,
				runnerdomain.SessionStatusRunning:
				return errorResult("session is already live"), nil
			}
			// allowed: failed, succeeded, cancelled, idle

			if r.deps.Controller == nil {
				return errorResult("controller not available"), nil
			}
			if err := r.deps.Controller.Recover(ctx, req.SessionID, sess.RoleKey); err != nil {
				if errors.Is(err, agentsessiondomain.ErrNotResumable) {
					return errorResult("session not resumable"), nil
				}
				return errorResult("recover: " + err.Error()), nil
			}

			// Append audit message with the caller's role key.
			msg := fmt.Sprintf("recovered by agent %s", sess.RoleKey)
			_, _ = r.deps.Runner.AppendMessage(ctx, &runnerdomain.Message{
				SessionID: req.SessionID,
				Kind:      runnerdomain.MessageKindSystem,
				Content:   msg,
			})

			return textResult(map[string]any{
				"session_id": req.SessionID,
				"status":     string(runnerdomain.SessionStatusPending),
				"recovered":  true,
			}), nil
		},
	}
}


// issueAttachmentUploadTool declares the issue_attachment_upload tool.
// File bytes arrive via multipart/form-data (handled by the HTTP handler,
// which calls Registry.UploadAttachment). The JSON code path returns a
// clear error directing callers to use multipart.
func (r *Registry) issueAttachmentUploadTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_attachment_upload",
		Description: "Upload a file from the workspace as an issue attachment. Use multipart/form-data with parts: `file` (binary, required), `display_name` (optional), `inline` (boolean, default false), `comment_id` (optional). Returns attachment metadata including an `attachment_id` and `markdown_snippet` — use `issue_comment` to insert the snippet into a comment body.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Original filename (e.g. 'screenshot.png'). Required.",
				},
				"display_name": map[string]any{
					"type":        "string",
					"description": "Optional display name for the attachment. Defaults to `name`.",
				},
				"inline": map[string]any{
					"type":        "boolean",
					"description": "When true, produces inline syntax `![attachment:N]` for images/videos. Default false.",
				},
				"comment_id": map[string]any{
					"type":        "integer",
					"description": "Optional comment ID to bind the attachment to an existing comment.",
				},
			},
			"required": []string{"name"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			return errorResult("issue_attachment_upload requires multipart/form-data — send file bytes as a `file` part, not JSON"), nil
		},
	}
}

// UploadAttachment handles multipart file upload for the
// issue_attachment_upload tool. Called from the HTTP handler after
// parsing the multipart form. It loads the session scope, delegates to
// the attachment service, and returns the tool result.
func (r *Registry) UploadAttachment(
	ctx context.Context,
	sess *runnerdomain.AgentSession,
	fileBytes []byte,
	name, displayName string,
	inline bool,
	commentID int64,
) (platformmcpdomain.Result, error) {
	scope, err := r.loadScope(ctx, sess)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	if len(fileBytes) == 0 || name == "" {
		return errorResult("file and name are required"), nil
	}

	if displayName == "" {
		displayName = name
	}

	att, err := r.deps.Attachments.UploadAttachment(ctx, &issuedomain.AttachmentUploadParams{
		RepoID:      scope.repo.ID,
		IssueID:     scope.issue.ID,
		Data:        fileBytes,
		Name:        name,
		DisplayName: displayName,
		Inline:      inline,
		CommentID:   commentID,
		AgentRole:   sess.RoleKey,
	})
	if err != nil {
		return errorResult("upload attachment: " + err.Error()), nil
	}

	return textResult(map[string]any{
		"attachment_id":    att.ID,
		"display_name":     displayName,
		"original_name":    att.OriginalName,
		"size_bytes":       att.SizeBytes,
		"mime_type":        att.DetectedMimeType,
		"kind":             string(att.Kind),
		"markdown_snippet": attachmentMarkdownSnippet(att.ID, att.Kind, inline),
	}), nil
}

// attachmentMarkdownSnippet returns the markdown token for an attachment.
func attachmentMarkdownSnippet(id int64, kind issuedomain.AttachmentKind, inline bool) string {
	if inline && (kind == issuedomain.AttachmentKindImage || kind == issuedomain.AttachmentKindVideo) {
		return fmt.Sprintf("![attachment:%d]", id)
	}
	return fmt.Sprintf("[attachment:%d]", id)
}




// issueCreateTool creates a new issue (optionally as a child of the
// current issue). When parent=true the new issue's base_branch is set
// to the current issue's branch so merging a child fast-forwards into
// the parent's working line. Author is the agent (authorID=0).
func (r *Registry) issueCreateTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_create",
		Description: "Create a new issue (optionally as a child of the current one). Returns the new issue's number, title, state, and branch_name.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{
					"type":        "string",
					"description": "Title of the new issue (required, 1-200 chars).",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Optional body text for the new issue.",
				},
				"parent": map[string]any{
					"type":        "boolean",
					"description": "When true, create as a child of the current issue. Default false.",
				},
			},
			"required": []string{"title"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			var req struct {
				Title  string `json:"title"`
				Body   string `json:"body"`
				Parent bool   `json:"parent"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			title := strings.TrimSpace(req.Title)
			if title == "" || len(title) > 200 {
				return errorResult("title is required (1-200 chars)"), nil
			}

			baseBranch := scope.repo.DefaultBranch
			var parentID, parentNumber int64
			if req.Parent {
				baseBranch = scope.issue.BranchName
				parentID = scope.issue.ID
				parentNumber = scope.issue.Number
			}

			iss, err := r.deps.Issues.Create(ctx, scope.repo.ID, 0, title, req.Body, baseBranch, sess.RoleKey, parentID, parentNumber)
			if err != nil {
				return errorResult("create issue: " + err.Error()), nil
			}

			// Fire issue.opened so subscribing roles wake.
			if r.deps.Spawner != nil {
				_, _ = r.deps.Spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
					Trigger:     agentsconfig.TriggerIssueOpened,
					CauseKind:   agentsessiondomain.CauseKindIssueOpened,
					CauseID:     "",
					RepoID:      scope.repo.ID,
					IssueNumber: int32(iss.Number),
					ActorID:     sess.CreatedBy,
				})
			}

			return textResult(map[string]any{
				"number":      iss.Number,
				"title":       iss.Title,
				"state":       string(iss.State),
				"branch_name": iss.BranchName,
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
