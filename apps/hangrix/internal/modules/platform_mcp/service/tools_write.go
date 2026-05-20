package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
		if agentsconfig.HasBacktickWrappedMention(body) {
			return errorResult("body contains an @agent-<role> mention wrapped in backticks — remove the backticks around the mention so the parser can see it, or omit the mention entirely"), nil
		}

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


// issueAttachmentUploadTool uploads a workspace file as an issue attachment.
// The agent passes a workspace-relative or absolute `path`; the handler reads
// the file from the server's repo clone, detects MIME type, and uploads it
// to the issue attachments API.
//
// Returns attachment metadata including an attachment_id and markdown_snippet
// the agent can insert into a subsequent issue_comment call.
func (r *Registry) issueAttachmentUploadTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_attachment_upload",
		Description: "Upload a file from the workspace as an issue attachment. Returns attachment metadata including an `attachment_id` and `markdown_snippet` — use `issue_comment` to insert the snippet into a comment body. `path` must be a workspace-relative or absolute path to an existing file. Set `inline` to true for images/videos you want rendered inline (produces `![attachment:N]` syntax); false / omitted produces `[attachment:N]` link syntax.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Workspace-relative or absolute path to the file to upload. Required.",
				},
				"display_name": map[string]any{
					"type":        "string",
					"description": "Optional display name for the attachment. Defaults to the file's basename.",
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
			"required": []string{"path"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			var req struct {
				Path        string `json:"path"`
				DisplayName string `json:"display_name"`
				Inline      bool   `json:"inline"`
				CommentID   int64  `json:"comment_id"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			req.Path = strings.TrimSpace(req.Path)
			if req.Path == "" {
				return errorResult("path is required"), nil
			}

			cleanPath, absPath, err := resolveAttachmentPath(scope.fsPath, req.Path)
			if err != nil {
				return errorResult("resolve path: " + err.Error()), nil
			}

			fi, err := os.Stat(absPath)
			if err != nil {
				if os.IsNotExist(err) {
					return errorResult(fmt.Sprintf("file not found: %s", cleanPath)), nil
				}
				return errorResult("stat file: " + err.Error()), nil
			}
			if fi.IsDir() {
				return errorResult(fmt.Sprintf("path is a directory, not a file: %s", cleanPath)), nil
			}

			mimeType, err := detectMimeType(absPath)
			if err != nil {
				return errorResult("detect MIME type: " + err.Error()), nil
			}

			sha256Hex, err := sha256File(absPath)
			if err != nil {
				return errorResult("sha256: " + err.Error()), nil
			}

			displayName := strings.TrimSpace(req.DisplayName)
			if displayName == "" {
				displayName = filepath.Base(cleanPath)
			}

			// When the attachment module lands (server work, issue #38),
			// the upload path below will be used. Until then, return
			// file metadata so the agent knows the file was found.
			if r.deps.Attachments == nil {
				return textResult(map[string]any{
					"staged":       true,
					"path":         cleanPath,
					"display_name": displayName,
					"size_bytes":   fi.Size(),
					"mime_type":    mimeType,
					"sha256":       sha256Hex,
					"inline":       req.Inline,
					"note":         "attachment backend not yet available — file validated, pending server module",
				}), nil
			}

			// When the attachment module is ready, this path uploads the file:
			att, err := r.deps.Attachments.Upload(ctx, AttachmentUploadParams{
				RepoID:      scope.repo.ID,
				IssueID:     scope.issue.ID,
				FilePath:    absPath,
				DisplayName: displayName,
				Inline:      req.Inline,
				CommentID:   req.CommentID,
				AgentRole:   sess.RoleKey,
				SizeBytes:   fi.Size(),
				MimeType:    mimeType,
				SHA256:      sha256Hex,
			})
			if err != nil {
				return errorResult("upload attachment: " + err.Error()), nil
			}

			return textResult(map[string]any{
				"attachment_id":    att.ID,
				"display_name":     att.DisplayName,
				"size_bytes":       att.SizeBytes,
				"mime_type":        att.MimeType,
				"download_url":     att.DownloadURL,
				"preview_url":      att.PreviewURL,
				"markdown_snippet": att.MarkdownSnippet,
			}), nil
		},
	}
}

// ---- attachment helpers ----

// AttachmentUploader is the seam between the platform_mcp tool and the
// attachment module. The attachment module (being built in issue #38)
// will provide an implementation bound via ioc. nil means the module is
// not yet available and the tool returns a "staged" response.
type AttachmentUploader interface {
	Upload(ctx context.Context, params AttachmentUploadParams) (*AttachmentResult, error)
}

// AttachmentUploadParams carries everything the attachment module needs
// to store the file and create the DB row.
type AttachmentUploadParams struct {
	RepoID      int64
	IssueID     int64
	FilePath    string // absolute path to the temp source file
	DisplayName string
	Inline      bool
	CommentID   int64
	AgentRole   string
	SizeBytes   int64
	MimeType    string
	SHA256      string
}

// AttachmentResult is the subset of the attachment row the tool returns
// to the agent.
type AttachmentResult struct {
	ID              int64
	DisplayName     string
	SizeBytes       int64
	MimeType        string
	DownloadURL     string
	PreviewURL      string
	MarkdownSnippet string
}

// resolveAttachmentPath normalises the agent-supplied path relative to
// the repo's filesystem root (scope.fsPath). Rejects paths that escape
// the repo root (e.g. ../../etc/passwd).
func resolveAttachmentPath(repoRoot, agentPath string) (cleanRel, abs string, err error) {
	p := filepath.Clean(agentPath)
	if filepath.IsAbs(p) {
		// Absolute path: strip the leading slash and join with repo root.
		// The path must still be within the repo after resolution.
		p = p[1:] // strip leading /
	}
	abs = filepath.Join(repoRoot, p)
	// Double-check the resolved path is still within repoRoot.
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", fmt.Errorf("path escapes repository root: %s", agentPath)
	}
	return rel, abs, nil
}

// detectMimeType reads the first 512 bytes and uses net/http to sniff
// the content type. Falls back to "application/octet-stream".
func detectMimeType(absPath string) (string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	return http.DetectContentType(buf[:n]), nil
}

// sha256File computes the hex-encoded SHA-256 digest of the file.
func sha256File(absPath string) (string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
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
