package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	platformmcpdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_mcp/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// issuePatchSubmitTool lets an agent submit a patch against the current
// issue's branch. The server parses the unified diff to extract
// changed_paths, file_count, additions, and deletions for caching.
// A stale patch (base_head_sha != issue.head_sha) is still recorded but
// marked 'stale'; submitted otherwise.
func (r *Registry) issuePatchSubmitTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_patch_submit",
		Description: "Submit a unified diff patch to the current issue. The patch is stored for review and may be applied by a maintainer. Never push to the remote branch directly — submit your work as a patch instead.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{
					"type":        "string",
					"description": "Short title describing this patch (1-200 chars).",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Optional longer description of what the patch does and why.",
				},
				"base_head_sha": map[string]any{
					"type":        "string",
					"description": "The issue branch head SHA this patch is based on. Use `git rev-parse <base_branch>` to get this.",
				},
				"patch": map[string]any{
					"type":        "string",
					"description": "The unified diff text. Generate with `git diff <base_branch>...HEAD`.",
				},
			},
			"required": []string{"title", "base_head_sha", "patch"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			var req struct {
				Title       string `json:"title"`
				Description string `json:"description"`
				BaseHeadSHA string `json:"base_head_sha"`
				Patch       string `json:"patch"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			title := strings.TrimSpace(req.Title)
			if title == "" || len(title) > 200 {
				return errorResult("title is required (1-200 chars)"), nil
			}
			patch := strings.TrimSpace(req.Patch)
			if patch == "" {
				return errorResult("patch is required"), nil
			}
			baseHeadSHA := strings.TrimSpace(req.BaseHeadSHA)
			if baseHeadSHA == "" {
				return errorResult("base_head_sha is required"), nil
			}

			// Parse the patch to extract changed_paths, stats.
			changedPaths, fileCount, additions, deletions := parsePatchStats(patch)

			// Determine initial status: stale if base_head_sha != current issue head.
			status := issuedomain.PatchStatusSubmitted
			if scope.issue.HeadSHA != "" && baseHeadSHA != scope.issue.HeadSHA {
				status = issuedomain.PatchStatusStale
			}

			p := &issuedomain.PatchSubmission{
				RepoID:       scope.repo.ID,
				IssueID:      scope.issue.ID,
				SessionID:    sess.ID,
				AgentRole:    sess.RoleKey,
				BaseHeadSHA:  baseHeadSHA,
				Title:        title,
				Description:  strings.TrimSpace(req.Description),
				PatchText:     patch,
				ChangedPaths:  changedPaths,
				FileCount:     fileCount,
				Additions:     additions,
				Deletions:     deletions,
				Status:        status,
			}

			created, err := r.deps.Patches.CreatePatch(ctx, p)
			if err != nil {
				return errorResult("create patch: " + err.Error()), nil
			}

			// Supersede previous submitted patches from the same role.
			_, _ = r.deps.Patches.SupersedePatches(ctx, scope.issue.ID, sess.RoleKey, created.ID)

			// Write timeline event.
			payload, _ := json.Marshal(issuedomain.PatchEventPayload{
				SubmissionID: created.ID,
				Title:        created.Title,
				AgentRole:    created.AgentRole,
			})
			_, _ = r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventPatchSubmitted, payload, sess.RoleKey)

			// Fire patch.submitted trigger.
			r.firePatchSubmitted(ctx, sess, scope, created)

			return textResult(map[string]any{
				"id":             created.ID,
				"status":         string(created.Status),
				"title":          created.Title,
				"file_count":     created.FileCount,
				"additions":      created.Additions,
				"deletions":      created.Deletions,
				"changed_paths":  created.ChangedPaths,
				"base_head_sha":  created.BaseHeadSHA,
				"stale":          status == issuedomain.PatchStatusStale,
				"created_at":     stableTime(created.CreatedAt),
			}), nil
		},
	}
}

// firePatchSubmitted dispatches the patch.submitted trigger so
// reviewer / tester / maintainer roles wake. ChangedPaths is carried
// so per-role PushFilter (paths / paths_ignore) can be evaluated.
func (r *Registry) firePatchSubmitted(ctx context.Context, sess *runnerdomain.AgentSession, scope *sessionScope, patch *issuedomain.PatchSubmission) {
	if r.deps.Spawner == nil {
		return
	}
	triggerPayload, _ := json.Marshal(map[string]any{
		"submission_id": patch.ID,
		"title":         patch.Title,
		"description":   patch.Description,
		"agent_role":    patch.AgentRole,
		"base_head_sha": patch.BaseHeadSHA,
	})
	_, _ = r.deps.Spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
		Trigger:      agentsconfig.TriggerPatchSubmitted,
		CauseKind:    agentsessiondomain.CauseKindPatchSubmitted,
		CauseID:      strconv.FormatInt(patch.ID, 10),
		RepoID:       scope.repo.ID,
		IssueNumber:  *sess.IssueNumber,
		ActorID:      sess.CreatedBy,
		ChangedPaths: patch.ChangedPaths,
		Payload:      triggerPayload,
	})
}

// issuePatchListTool returns all patches for the current issue.
func (r *Registry) issuePatchListTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_patch_list",
		Description: "List all patch submissions on the current issue, newest first. Each entry includes id, title, status, agent_role, file_count, additions, deletions, and timestamps.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			patches, err := r.deps.Patches.ListPatches(ctx, scope.issue.ID)
			if err != nil {
				return errorResult("list patches: " + err.Error()), nil
			}
			items := make([]map[string]any, 0, len(patches))
			for _, p := range patches {
				items = append(items, patchSummary(p))
			}
			return textResult(map[string]any{"patches": items}), nil
		},
	}
}

// issuePatchReadTool returns metadata + full diff for a single patch.
func (r *Registry) issuePatchReadTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_patch_read",
		Description: "Read a single patch submission's metadata and full unified diff. Use the id from issue_patch_list.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "integer",
					"description": "Patch submission ID to read.",
				},
			},
			"required": []string{"id"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			var req struct {
				ID int64 `json:"id"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			if req.ID <= 0 {
				return errorResult("id is required and must be positive"), nil
			}
			patch, err := r.deps.Patches.GetPatch(ctx, req.ID)
			if err != nil {
				return errorResult("get patch: " + err.Error()), nil
			}
			if patch.IssueID != scope.issue.ID {
				return errorResult("patch does not belong to the current issue"), nil
			}
			return textResult(map[string]any{
				"summary": patchSummary(patch),
				"patch":   patch.PatchText,
			}), nil
		},
	}
}

// issuePatchApplyTool applies a patch to the issue branch. Only patches
// with status=submitted and base_head_sha == issue.head_sha can be applied.
// Creates a real commit on the issue branch and records a patch_applied event.
func (r *Registry) issuePatchApplyTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_patch_apply",
		Description: "Apply a patch submission to the issue branch. Only patches with status='submitted' and base_head_sha matching the current issue head can be applied. Creates a commit on the issue branch authored by the original submitter. Requires `issue_patch_apply` in the role's `can:` whitelist.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "integer",
					"description": "Patch submission ID to apply.",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Optional commit message. Defaults to 'Apply patch: <title>'.",
				},
			},
			"required": []string{"id"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			var req struct {
				ID      int64  `json:"id"`
				Message string `json:"message"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			if req.ID <= 0 {
				return errorResult("id is required and must be positive"), nil
			}

			patch, err := r.deps.Patches.GetPatch(ctx, req.ID)
			if err != nil {
				return errorResult("get patch: " + err.Error()), nil
			}
			if patch.IssueID != scope.issue.ID {
				return errorResult("patch does not belong to the current issue"), nil
			}

			// Gate: only submitted patches with matching base_head_sha can be applied.
			if patch.Status != issuedomain.PatchStatusSubmitted {
				return errorResult(fmt.Sprintf("cannot apply patch with status '%s' — must be 'submitted'", patch.Status)), nil
			}
			if scope.issue.HeadSHA != "" && patch.BaseHeadSHA != scope.issue.HeadSHA {
				return errorResult("patch base_head_sha does not match current issue head_sha — patch is stale"), nil
			}

			// Apply the patch using git.
			msg := strings.TrimSpace(req.Message)
			if msg == "" {
				msg = fmt.Sprintf("Apply patch: %s", patch.Title)
			}

			// Author identity: preserve the original submitter's role identity.
			authorIdentity := agentsessiondomain.IdentityForRole(patch.AgentRole, "")
			committerIdentity := agentsessiondomain.IdentityForRole(sess.RoleKey, "")

			commitSHA, err := r.deps.Git.ApplyPatch(
				scope.fsPath,
				scope.issue.BranchName,
				patch.PatchText,
				msg,
				gitdomain.Signature{
					Name:  authorIdentity.Name,
					Email: authorIdentity.Email,
					When:  time.Now(),
				},
				gitdomain.Signature{
					Name:  committerIdentity.Name,
					Email: committerIdentity.Email,
					When:  time.Now(),
				},
			)
			if err != nil {
				return errorResult("apply patch: " + err.Error()), nil
			}

			// Update patch status.
			updated, err := r.deps.Patches.UpdatePatchStatus(ctx, patch.ID, issuedomain.PatchStatusApplied, commitSHA, "")
			if err != nil {
				return errorResult("update patch status: " + err.Error()), nil
			}

			// Sync the issue's HeadSHA to reflect the new commit.
			_ = r.deps.Issues.UpdateHeadSHA(ctx, scope.issue.ID, commitSHA)

			// Write timeline event.
			payload, _ := json.Marshal(issuedomain.PatchEventPayload{
				SubmissionID: updated.ID,
				Title:        updated.Title,
				AgentRole:    updated.AgentRole,
				CommitSHA:    commitSHA,
			})
			_, _ = r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventPatchApplied, payload, sess.RoleKey)

			// Mark other submitted patches as stale since the issue head moved.
			_, _ = r.deps.Patches.MarkStalePatches(ctx, scope.issue.ID, commitSHA)

			return textResult(map[string]any{
				"id":          updated.ID,
				"status":      string(updated.Status),
				"commit_sha":  commitSHA,
				"applied_at":  stableTime(*updated.AppliedAt),
			}), nil
		},
	}
}

// issuePatchRejectTool rejects a patch submission. Only patches with
// status=submitted can be rejected. A patch_rejected event is recorded.
func (r *Registry) issuePatchRejectTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "issue_patch_reject",
		Description: "Reject a patch submission. Only patches with status='submitted' can be rejected. The patch is not applied; a rejection reason is recorded. Requires `issue_patch_reject` in the role's `can:` whitelist.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "integer",
					"description": "Patch submission ID to reject.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Reason for rejection (required, shown on timeline).",
				},
			},
			"required": []string{"id", "reason"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			var req struct {
				ID     int64  `json:"id"`
				Reason string `json:"reason"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			if req.ID <= 0 {
				return errorResult("id is required and must be positive"), nil
			}
			reason := strings.TrimSpace(req.Reason)
			if reason == "" {
				return errorResult("reason is required"), nil
			}

			patch, err := r.deps.Patches.GetPatch(ctx, req.ID)
			if err != nil {
				return errorResult("get patch: " + err.Error()), nil
			}
			if patch.IssueID != scope.issue.ID {
				return errorResult("patch does not belong to the current issue"), nil
			}
			if patch.Status != issuedomain.PatchStatusSubmitted {
				return errorResult(fmt.Sprintf("cannot reject patch with status '%s' — must be 'submitted'", patch.Status)), nil
			}

			updated, err := r.deps.Patches.UpdatePatchStatus(ctx, patch.ID, issuedomain.PatchStatusRejected, "", reason)
			if err != nil {
				return errorResult("update patch status: " + err.Error()), nil
			}

			// Write timeline event.
			payload, _ := json.Marshal(issuedomain.PatchEventPayload{
				SubmissionID: updated.ID,
				Title:        updated.Title,
				AgentRole:    updated.AgentRole,
				Reason:       reason,
			})
			_, _ = r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventPatchRejected, payload, sess.RoleKey)

			return textResult(map[string]any{
				"id":     updated.ID,
				"status": string(updated.Status),
				"reason": updated.RejectedReason,
			}), nil
		},
	}
}

// parsePatchStats extracts file stats from a unified diff. Returns
// changed_paths, file_count, additions, deletions. Best-effort: on
// parse failure returns zero values so the patch is still recorded.
func parsePatchStats(patch string) (paths []string, fileCount, additions, deletions int32) {
	lines := strings.Split(patch, "\n")
	seenPaths := make(map[string]struct{})
	for _, line := range lines {
		if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
			// Extract filename from "+++ b/path" or "--- a/path"
			parts := strings.SplitN(line, "/", 2)
			if len(parts) == 2 {
				p := parts[1]
				if p != "" && p != "/dev/null" {
					if _, ok := seenPaths[p]; !ok {
						seenPaths[p] = struct{}{}
						paths = append(paths, p)
					}
				}
			}
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			additions++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			deletions++
		}
	}
	fileCount = int32(len(paths))
	return
}

func patchSummary(p *issuedomain.PatchSubmission) map[string]any {
	return map[string]any{
		"id":              p.ID,
		"issue_id":        p.IssueID,
		"session_id":      p.SessionID,
		"agent_role":      p.AgentRole,
		"title":           p.Title,
		"description":     p.Description,
		"base_head_sha":   p.BaseHeadSHA,
		"status":          string(p.Status),
		"file_count":      p.FileCount,
		"additions":       p.Additions,
		"deletions":       p.Deletions,
		"changed_paths":   p.ChangedPaths,
		"applied_commit_sha": p.AppliedCommitSHA,
		"rejected_reason": p.RejectedReason,
		"created_at":      stableTime(p.CreatedAt),
		"updated_at":      stableTime(p.UpdatedAt),
	}
}
