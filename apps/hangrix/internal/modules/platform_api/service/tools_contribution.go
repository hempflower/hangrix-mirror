package service

import (
	"context"
	"strings"

	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
)

// Contribution-branch business logic (docs/contribution-branches.md). A
// contribution is a git branch an agent pushes to its own per-issue namespace
// (refs/heads/issue-<N>/<role>); pushing creates/updates the contribution
// implicitly (handled server-side in the push observer). The helpers here back
// the v1 contribution surface: review-gate computation, the server-side merge
// into the issue branch, and sibling-mergeability refresh.

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
