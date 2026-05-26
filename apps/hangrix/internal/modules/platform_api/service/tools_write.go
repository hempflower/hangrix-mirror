package service

import (
	"context"
	"fmt"
	"log"
	"strings"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
	attachmentdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

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

// tryDeleteContributionBranches attempts to delete every contribution branch
// under the issue's namespace (issue-<N>/...) after a successful issue merge.
// It mirrors tryDeleteContributionBranches in the issue HTTP handler.  Best-
// effort: failures are logged but never prevent the merge from succeeding.
// Returns nil when there are no matching branches or ListRefs failed.
func (r *Registry) tryDeleteContributionBranches(ctx context.Context, repoID int64, fsPath string, issueNumber, issueID int64) []contribCleanupResult {
	refs, err := r.deps.Git.ListRefs(fsPath)
	if err != nil {
		log.Printf("platform_api: list refs for contribution cleanup repo=%d issue=%d: %v", repoID, issueNumber, err)
		return nil
	}

	prefix := fmt.Sprintf("issue-%d/", issueNumber)
	var results []contribCleanupResult

	for _, ref := range refs.Branches {
		if !strings.HasPrefix(ref.Name, prefix) {
			continue
		}
		result := contribCleanupResult{Branch: ref.Name}

		blocked := false

		// Check branch protections.
		if r.deps.Protections != nil {
			rules, err := r.deps.Protections.List(ctx, repoID)
			if err == nil {
				if rule := repodomain.MatchProtection(rules, ref.Name); rule != nil && rule.ForbidDelete {
					result.Deleted = false
					result.Reason = "protected"
					blocked = true
				}
			}
		}

		// Run branch-write guards.
		if !blocked {
			for _, g := range r.deps.Guards {
				if err := g.CheckBranchWrite(ctx, repodomain.BranchWriteOp{
					RepoID:     repoID,
					Branch:     ref.Name,
					OldSHA:     ref.SHA,
					IsDelete:   true,
					IsInternal: true,
				}); err != nil {
					result.Deleted = false
					result.Reason = "denied"
					blocked = true
					break
				}
			}
		}

		if !blocked {
			if err := r.deps.Git.DeleteBranch(fsPath, ref.Name); err != nil {
				log.Printf("platform_api: delete contribution branch %s repo=%d issue=%d: %v", ref.Name, repoID, issueNumber, err)
				result.Deleted = false
				result.Reason = "delete_failed"
			} else {
				result.Deleted = true
				// go-git Storer.RemoveReference bypasses hooks, so
				// SyncContribution never sees the zero-oid update.
				// Mark the contribution closed here explicitly.
				contribRef := "refs/heads/" + ref.Name
				if c, cerr := r.deps.Contributions.GetContributionByRef(ctx, issueID, contribRef); cerr == nil && c != nil && !c.Status.Terminal() {
					if _, cerr = r.deps.Contributions.SetContributionStatus(ctx, c.ID, issuedomain.ContribStatusClosed); cerr != nil {
						log.Printf("platform_api: close contribution %d after branch delete: %v", c.ID, cerr)
					}
				}
			}
		}
		results = append(results, result)
	}

	if len(results) == 0 {
		return nil
	}
	return results
}

// mergeCleanupResult duplicates mergeCleanup from the issue HTTP handler
// so the platform_api module stays decoupled from the issue handler package.
type mergeCleanupResult struct {
	Deleted bool   `json:"deleted"`
	Reason  string `json:"reason,omitempty"`
}

// contribCleanupResult mirrors contribCleanupResult from the issue HTTP handler.
type contribCleanupResult struct {
	Branch  string `json:"branch"`
	Deleted bool   `json:"deleted"`
	Reason  string `json:"reason,omitempty"`
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
) (apidomain.Result, error) {
	_, err := r.loadScope(ctx, sess)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	if len(fileBytes) == 0 || name == "" {
		return errorResult("file and name are required"), nil
	}

	if displayName == "" {
		displayName = name
	}

	att, err := r.deps.Attachments.Upload(ctx, &attachmentdomain.AttachmentUploadParams{
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

	url := fmt.Sprintf("/api/attachments/%d/download", att.ID)
	return textResult(map[string]any{
		"attachment_id":    att.ID,
		"display_name":     displayName,
		"original_name":    att.OriginalName,
		"size_bytes":       att.SizeBytes,
		"mime_type":        att.DetectedMimeType,
		"kind":             string(att.Kind),
		"url":              url,
		"markdown_snippet": attachmentMarkdownSnippet(att.ID, att.DisplayName, att.OriginalName, att.Kind, inline),
	}), nil
}

// attachmentMarkdownSnippet returns the markdown token for an attachment.
func attachmentMarkdownSnippet(id int64, displayName, originalName string, kind attachmentdomain.AttachmentKind, inline bool) string {
	if inline && (kind == attachmentdomain.AttachmentKindImage || kind == attachmentdomain.AttachmentKindVideo) {
		return fmt.Sprintf("![](/api/attachments/%d/download)", id)
	}
	name := displayName
	if name == "" {
		name = originalName
	}
	return fmt.Sprintf("[%s](/api/attachments/%d/download)", name, id)
}
