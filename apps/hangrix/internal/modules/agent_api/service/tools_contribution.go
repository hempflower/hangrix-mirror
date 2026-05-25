package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentapidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_api/domain"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// Contribution-branch tools (docs/contribution-branches.md). A contribution is
// a git branch an agent pushes to its own per-issue namespace
// (refs/heads/issue-<N>/<role>); pushing creates/updates the contribution
// implicitly (handled server-side in the push observer). These tools cover the
// read + lifecycle surface: listing, reading the server-computed diff/reviews,
// setting metadata, the server-side merge into the issue branch, and closing.

// contributionListTool lists the contributions on the current issue.
func (r *Registry) contributionListTool() *agentapidomain.Tool {
	return &agentapidomain.Tool{
		Name:        "contribution_list",
		Description: "List the contribution branches on the current issue. Each entry has id, agent_role, ref_name, status (pending/approved/rejected/merged/closed), mergeable, merge_mode, head_sha, and diff stats. By default only non-terminal contributions (pending, approved, rejected) are returned — use include_closed / include_merged to also see closed or merged contributions. A contribution is created automatically when you push to refs/heads/issue-<N>/<your-role> — the git push response includes the contribution_id directly, so you don't need this tool just to discover your ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"include_closed": map[string]any{"type": "boolean", "description": "When true, also return contributions with status 'closed'. Default false."},
				"include_merged": map[string]any{"type": "boolean", "description": "When true, also return contributions with status 'merged'. Default false."},
			},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (agentapidomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			var req struct {
				IncludeClosed bool `json:"include_closed"`
				IncludeMerged bool `json:"include_merged"`
			}
			_ = unmarshalArgs(args, &req)
			contribs, err := r.deps.Contributions.ListContributions(ctx, scope.issue.ID, req.IncludeClosed, req.IncludeMerged)
			if err != nil {
				return errorResult("list contributions: " + err.Error()), nil
			}
			items := make([]map[string]any, 0, len(contribs))
			for _, c := range contribs {
				items = append(items, contributionSummary(c))
			}
			return textResult(map[string]any{"contributions": items}), nil
		},
	}
}

// contributionReadTool returns contribution metadata, review status, and a
// local-checkout hint. It no longer returns a server-computed diff; the agent
// should fetch the branch and run git locally to inspect changes.
func (r *Registry) contributionReadTool() *agentapidomain.Tool {
	return &agentapidomain.Tool{
		Name:        "contribution_read",
		Description: "Read one contribution: metadata, review status (verdict plus which required reviewers still must vote), and a checkout_hint to fetch the branch and compare locally. This tool no longer returns an inline diff — use git locally after fetching. Use the id from contribution_list.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "integer", "description": "Contribution id to read (from contribution_list)."},
			},
			"required": []string{"id"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (agentapidomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			c, errRes := r.loadContribution(ctx, scope, args)
			if errRes != nil {
				return *errRes, nil
			}

			contribBranch := strings.TrimPrefix(c.RefName, "refs/heads/")
			checkoutHint := fmt.Sprintf(
				"This tool no longer returns an inline diff. To view the changes locally, fetch the contribution branch and compare with the issue branch:\n\n  git fetch origin %s\n  git diff origin/%s...origin/%s\n\nOr checkout directly:\n\n  git fetch origin %s && git checkout %s",
				contribBranch, scope.issue.BranchName, contribBranch,
				contribBranch, contribBranch,
			)

			var review *issuedomain.ReviewStatus
			if events, err := r.deps.Issues.ListEvents(ctx, scope.issue.ID); err == nil {
				review = issuedomain.ComputeContributionReviewStatus(c, r.requiredReviewers(ctx, scope.repo.ID, c), events)
			}
			return textResult(map[string]any{
				"contribution":  contributionSummary(c),
				"review":        review,
				"checkout_hint": checkoutHint,
			}), nil
		},
	}
}

// contributionSetMetaTool lets the owning role set the title/description of its
// own contribution (the PR title/body).
func (r *Registry) contributionSetMetaTool() *agentapidomain.Tool {
	return &agentapidomain.Tool{
		Name:        "contribution_set_meta",
		Description: "Set the title and description of your own contribution branch (its merge-request title/body). Only the role that owns the branch may set its metadata.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":          map[string]any{"type": "integer", "description": "Contribution id."},
				"title":       map[string]any{"type": "string", "description": "Short title (1-200 chars)."},
				"description": map[string]any{"type": "string", "description": "Optional longer description."},
			},
			"required": []string{"id", "title"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (agentapidomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			var req struct {
				ID          int64  `json:"id"`
				Title       string `json:"title"`
				Description string `json:"description"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			title := strings.TrimSpace(req.Title)
			if title == "" || len(title) > 200 {
				return errorResult("title is required (1-200 chars)"), nil
			}
			c, errRes := r.getContributionScoped(ctx, scope, req.ID)
			if errRes != nil {
				return *errRes, nil
			}
			if c.AgentRole != sess.RoleKey {
				return errorResult("only the owning role can set this contribution's metadata"), nil
			}
			updated, err := r.deps.Contributions.SetContributionMeta(ctx, c.ID, title, strings.TrimSpace(req.Description))
			if err != nil {
				return errorResult("set meta: " + err.Error()), nil
			}
			return textResult(contributionSummary(updated)), nil
		},
	}
}

// contributionApplyTool is the first-level gate: it merges an approved
// contribution branch into the issue branch, server-side. The commit SHA is
// computed by the server (no agent push). Gated by the `contribution_apply`
// capability in the role's `can:` whitelist (the maintainer role).
func (r *Registry) contributionApplyTool() *agentapidomain.Tool {
	return &agentapidomain.Tool{
		Name:        "contribution_apply",
		Description: "Merge an approved contribution branch into the issue branch (first-level gate). The server validates the review gate + mergeability and computes the merge commit — there is no agent push. Requires `contribution_apply` in the role's `can:` whitelist.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":      map[string]any{"type": "integer", "description": "Contribution id to merge."},
				"message": map[string]any{"type": "string", "description": "Optional merge commit message."},
			},
			"required": []string{"id"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (agentapidomain.Result, error) {
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
			c, errRes := r.getContributionScoped(ctx, scope, req.ID)
			if errRes != nil {
				return *errRes, nil
			}
			if c.Status.Terminal() {
				return errorResult(fmt.Sprintf("contribution is %s", c.Status)), nil
			}

			// First-level review gate.
			events, err := r.deps.Issues.ListEvents(ctx, scope.issue.ID)
			if err != nil {
				return errorResult("list events: " + err.Error()), nil
			}
			if rs := issuedomain.ComputeContributionReviewStatus(c, r.requiredReviewers(ctx, scope.repo.ID, c), events); rs.MergeBlocked {
				blockJSON, _ := json.Marshal(map[string]any{"error": "merge blocked", "block_reason": rs.BlockReason})
				return agentapidomain.Result{Text: string(blockJSON), IsError: true}, nil
			}

			contribBranch := strings.TrimPrefix(c.RefName, "refs/heads/")
			mergeable, mode, hint, _ := r.deps.Git.CheckAutoMerge(scope.fsPath, scope.issue.BranchName, contribBranch)
			if !mergeable {
				_ = r.deps.Contributions.SetContributionMergeable(ctx, c.ID, false, mode)
				return errorResult("contribution is not mergeable: " + hint), nil
			}

			msg := strings.TrimSpace(req.Message)
			if msg == "" {
				msg = fmt.Sprintf("Merge contribution %s into %s (issue #%d)", contribBranch, scope.issue.BranchName, scope.issue.Number)
			}
			identity := agentsessiondomain.IdentityForRole(sess.RoleKey, "")
			mergeSHA, mergedMode, err := r.deps.Git.MergeBranch(scope.fsPath, scope.issue.BranchName, contribBranch, msg, gitdomain.Signature{
				Name: identity.Name, Email: identity.Email, When: time.Now(),
			})
			if err != nil {
				if errors.Is(err, gitdomain.ErrMergeConflict) {
					_ = r.deps.Contributions.SetContributionMergeable(ctx, c.ID, false, "conflicted")
					return errorResult(fmt.Sprintf("merge conflict — contributor must rebase onto the latest `issue/%d` and push a NEW slug (this branch is immutable and now marked unmergeable)", scope.issue.Number)), nil
				}
				return errorResult("merge: " + err.Error()), nil
			}

			_ = r.deps.Issues.UpdateHeadSHA(ctx, scope.issue.ID, mergeSHA)
			merged, err := r.deps.Contributions.MarkContributionMerged(ctx, c.ID, mergeSHA)
			if err != nil {
				return errorResult("mark merged: " + err.Error()), nil
			}

			evtPayload, _ := json.Marshal(issuedomain.ContributionEventPayload{
				ContributionID: merged.ID, AgentRole: merged.AgentRole, RefName: merged.RefName,
				Title: merged.Title, MergeCommitSHA: mergeSHA,
			})
			_, _ = r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventContributionMerged, evtPayload, sess.RoleKey)

			// Landing one branch may conflict the siblings; refresh their flags.
			r.refreshSiblingMergeability(ctx, scope, merged.ID)

			return textResult(map[string]any{
				"id":        merged.ID,
				"status":    string(merged.Status),
				"merge_sha": mergeSHA,
				"mode":      mergedMode,
			}), nil
		},
	}
}

// contributionCloseTool lets the owning role abandon its contribution branch.
func (r *Registry) contributionCloseTool() *agentapidomain.Tool {
	return &agentapidomain.Tool{
		Name:        "contribution_close",
		Description: "Close (abandon) your own contribution branch. Only the owning role may close it; merged contributions cannot be closed.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":     map[string]any{"type": "integer", "description": "Contribution id to close."},
				"reason": map[string]any{"type": "string", "description": "Optional rationale, recorded on the timeline."},
			},
			"required": []string{"id"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (agentapidomain.Result, error) {
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
			c, errRes := r.getContributionScoped(ctx, scope, req.ID)
			if errRes != nil {
				return *errRes, nil
			}
			if c.AgentRole != sess.RoleKey {
				return errorResult("only the owning role can close this contribution"), nil
			}
			if c.Status.Terminal() {
				return errorResult(fmt.Sprintf("contribution is %s", c.Status)), nil
			}
			updated, err := r.deps.Contributions.SetContributionStatus(ctx, c.ID, issuedomain.ContribStatusClosed)
			if err != nil {
				return errorResult("close: " + err.Error()), nil
			}
			evtPayload, _ := json.Marshal(issuedomain.ContributionEventPayload{
				ContributionID: updated.ID, AgentRole: updated.AgentRole, RefName: updated.RefName, Reason: strings.TrimSpace(req.Reason),
			})
			_, _ = r.deps.Issues.CreateAgentEvent(ctx, scope.issue.ID, issuedomain.EventContributionClosed, evtPayload, sess.RoleKey)
			return textResult(map[string]any{"id": updated.ID, "status": string(updated.Status)}), nil
		},
	}
}

// loadContribution reads the {id} arg and resolves it to a contribution scoped
// to the caller's issue. Returns a tool error Result on any failure.
func (r *Registry) loadContribution(ctx context.Context, scope *sessionScope, args json.RawMessage) (*issuedomain.Contribution, *agentapidomain.Result) {
	var req struct {
		ID int64 `json:"id"`
	}
	if err := unmarshalArgs(args, &req); err != nil {
		res := errorResult("invalid arguments: " + err.Error())
		return nil, &res
	}
	return r.getContributionScoped(ctx, scope, req.ID)
}

// getContributionScoped loads a contribution by id and verifies it belongs to
// the caller's issue.
func (r *Registry) getContributionScoped(ctx context.Context, scope *sessionScope, id int64) (*issuedomain.Contribution, *agentapidomain.Result) {
	if id <= 0 {
		res := errorResult("id is required and must be positive")
		return nil, &res
	}
	c, err := r.deps.Contributions.GetContribution(ctx, id)
	if err != nil {
		res := errorResult("get contribution: " + err.Error())
		return nil, &res
	}
	if c.IssueID != scope.issue.ID {
		res := errorResult("contribution does not belong to the current issue")
		return nil, &res
	}
	return c, nil
}

// refreshSiblingMergeability recomputes mergeability for the issue's other open
// contributions against the (now-advanced) issue head. Best-effort.
func (r *Registry) refreshSiblingMergeability(ctx context.Context, scope *sessionScope, exceptID int64) {
	contribs, err := r.deps.Contributions.ListContributions(ctx, scope.issue.ID, true, true)
	if err != nil {
		return
	}
	for _, c := range contribs {
		if c.ID == exceptID || c.Status.Terminal() {
			continue
		}
		branch := strings.TrimPrefix(c.RefName, "refs/heads/")
		mergeable, mode, _, err := r.deps.Git.CheckAutoMerge(scope.fsPath, scope.issue.BranchName, branch)
		if err != nil {
			continue
		}
		_ = r.deps.Contributions.SetContributionMergeable(ctx, c.ID, mergeable, mode)
	}
}

// requiredReviewers returns the reviewer role keys that must approve a
// contribution: path-matched from the host config's `reviewers:` block, minus
// the contribution author. Returns nil when the host has no reviewers config
// or the yaml can't be loaded.
func (r *Registry) requiredReviewers(ctx context.Context, repoID int64, c *issuedomain.Contribution) []string {
	if r.deps.Spawner == nil {
		return nil
	}
	cfg, err := r.deps.Spawner.LoadHostConfig(ctx, repoID)
	if err != nil || cfg == nil {
		return nil
	}
	return excludeRole(cfg.RequiredReviewers(c.ChangedPaths), c.AgentRole)
}

// recomputeContributionStatus recomputes a non-terminal contribution's cached
// status from its required reviewers + current votes and persists it when it
// changed. Best-effort.
func (r *Registry) recomputeContributionStatus(ctx context.Context, repoID int64, c *issuedomain.Contribution) {
	if c.Status.Terminal() {
		return
	}
	events, err := r.deps.Issues.ListEvents(ctx, c.IssueID)
	if err != nil {
		return
	}
	rs := issuedomain.ComputeContributionReviewStatus(c, r.requiredReviewers(ctx, repoID, c), events)
	if next := rs.ContributionStatus(); next != c.Status {
		_, _ = r.deps.Contributions.SetContributionStatus(ctx, c.ID, next)
	}
}

// excludeRole returns roles with every occurrence of role removed.
func excludeRole(roles []string, role string) []string {
	if role == "" || len(roles) == 0 {
		return roles
	}
	out := make([]string, 0, len(roles))
	for _, rk := range roles {
		if rk != role {
			out = append(out, rk)
		}
	}
	return out
}

// issueMergeBlock is the second-level (issue → base) gate: it returns a
// non-empty reason when the issue branch isn't ready to merge — either some
// contribution is still pending review, or the issue branch carries no changes
// (nothing applied into it yet). Empty string means ready.
func (r *Registry) issueMergeBlock(ctx context.Context, scope *sessionScope) string {
	contribs, err := r.deps.Contributions.ListContributions(ctx, scope.issue.ID, true, true)
	if err != nil {
		return "cannot evaluate contributions: " + err.Error()
	}
	return issuedomain.IssueMergeBlock(contribs, r.issueBranchAhead(scope))
}

// issueBranchAhead reports whether the issue branch has commits its base does
// not — i.e. whether there is anything to merge into base.
func (r *Registry) issueBranchAhead(scope *sessionScope) bool {
	head, err := r.deps.Git.ResolveCommit(scope.fsPath, scope.issue.BranchName)
	if err != nil || head == "" {
		return false
	}
	base, err := r.deps.Git.ResolveCommit(scope.fsPath, scope.issue.BaseBranch)
	if err != nil || base == "" {
		return true // no base to compare — let MergeBranch decide
	}
	if head == base {
		return false
	}
	// Ahead iff head is NOT reachable from base (head carries commits base lacks).
	isAnc, err := r.deps.Git.IsAncestor(scope.fsPath, head, base)
	if err != nil {
		return true
	}
	return !isAnc
}

// contributionSummary is the wire shape returned to agents for a contribution.
func contributionSummary(c *issuedomain.Contribution) map[string]any {
	out := map[string]any{
		"id":            c.ID,
		"issue_id":      c.IssueID,
		"agent_role":    c.AgentRole,
		"ref_name":      c.RefName,
		"head_sha":      c.HeadSHA,
		"base_sha":      c.BaseSHA,
		"title":         c.Title,
		"description":   c.Description,
		"status":        string(c.Status),
		"mergeable":     c.Mergeable,
		"merge_mode":    c.MergeMode,
		"changed_paths": c.ChangedPaths,
		"files":         c.Files,
		"additions":     c.Additions,
		"deletions":     c.Deletions,
		"created_at":    stableTime(c.CreatedAt),
		"updated_at":    stableTime(c.UpdatedAt),
	}
	if c.MergedCommitSHA != "" {
		out["merged_commit_sha"] = c.MergedCommitSHA
	}
	if c.MergedAt != nil {
		out["merged_at"] = stableTime(*c.MergedAt)
	}
	return out
}
