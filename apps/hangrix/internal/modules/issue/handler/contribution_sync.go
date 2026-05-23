package handler

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

// contributionRefRe matches a per-issue contribution namespace ref:
//
//	refs/heads/issue-<N>/<role>[/slug...]
//
// Group 1 is the issue number; group 2 is the rest of the path (the role key,
// optionally followed by a sub-slug). The role key is the first path segment
// of group 2 — that's the namespace owner. The full ref is what identifies
// the contribution, so multiple slugs under one role are distinct branches.
var contributionRefRe = regexp.MustCompile(`^refs/heads/issue-(\d+)/(.+)$`)

// SyncContribution upserts the contribution record for a freshly-pushed
// namespace ref and wakes reviewers. It is the contribution-branch analogue of
// SyncIssueBranch: the server computes the real diff (DiffMergeBase against the
// issue branch) and mergeability (CheckAutoMerge), so stats and path filters
// come from git rather than hand-parsed patch text.
//
// Best-effort: any error short-circuits this single ref without affecting the
// rest of the push (the client already has its response by PostReceive time).
func (h *Handler) SyncContribution(ctx context.Context, repo *repodomain.Repo, fsPath string, u repodomain.PushRefUpdate) {
	m := contributionRefRe.FindStringSubmatch(u.RefName)
	if m == nil {
		// Refs that look like a contribution attempt (issue-namespace-shaped)
		// but don't parse are logged — silent for ordinary refs (main, tags,
		// the issue/<n> branch) so we don't spam the log on every push.
		if strings.HasPrefix(u.RefName, "refs/heads/issue-") {
			log.Printf("issue: contribution ref %q did not match issue-<N>/<role>/<slug>; not recognised", u.RefName)
		}
		return
	}
	number, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || number <= 0 {
		log.Printf("issue: contribution ref %q has unparseable issue number; not recognised", u.RefName)
		return
	}
	role := strings.SplitN(m[2], "/", 2)[0]
	if role == "" {
		log.Printf("issue: contribution ref %q has empty role; not recognised", u.RefName)
		return
	}

	// Branch deletion (new SHA is the zero oid) — drop the contribution out of
	// the active list by closing it, if present.
	if isZeroOID(u.NewSHA) {
		if c, err := h.contributions.GetContributionByRef(ctx, contributionIssueID(ctx, h, repo.ID, number), u.RefName); err == nil && c != nil && !c.Status.Terminal() {
			_, _ = h.contributions.SetContributionStatus(ctx, c.ID, domain.ContribStatusClosed)
		}
		return
	}

	iss, err := h.issues.GetByNumber(ctx, repo.ID, number)
	if err != nil {
		log.Printf("issue: contribution ref %s -> issue #%d not found in repo %d: %v; not recognised", u.RefName, number, repo.ID, err)
		return
	}
	// Only accept contributions against an open issue.
	if iss.State != domain.StateOpen {
		log.Printf("issue: contribution ref %s -> issue #%d is %s (not open); not recognised", u.RefName, number, iss.State)
		return
	}
	log.Printf("issue: recognising contribution ref=%s issue=#%d role=%s head=%s", u.RefName, number, role, u.NewSHA)

	contribBranch := strings.TrimPrefix(u.RefName, "refs/heads/")
	headSHA := u.NewSHA
	if headSHA == "" {
		headSHA, _ = h.git.ResolveCommit(fsPath, contribBranch)
	}
	baseSHA, _ := h.git.ResolveCommit(fsPath, iss.BranchName)

	// Real diff of what the contribution adds relative to the issue branch.
	diffs, err := h.git.DiffMergeBase(fsPath, iss.BranchName, contribBranch)
	if err != nil {
		log.Printf("issue: contribution diff repo=%d issue=%d ref=%s: %v", repo.ID, number, u.RefName, err)
		diffs = nil
	}
	changedPaths, files, additions, deletions := contributionDiffStats(diffs)

	c, err := h.contributions.UpsertContributionOnPush(ctx, domain.ContributionUpsertParams{
		RepoID:       repo.ID,
		IssueID:      iss.ID,
		AgentRole:    role,
		RefName:      u.RefName,
		HeadSHA:      headSHA,
		BaseSHA:      baseSHA,
		ChangedPaths: changedPaths,
		Files:        files,
		Additions:    additions,
		Deletions:    deletions,
	})
	if err != nil {
		log.Printf("issue: upsert contribution repo=%d issue=%d ref=%s: %v", repo.ID, number, u.RefName, err)
		return
	}

	// Cache mergeability against the current issue head for the gate + UI.
	mergeable, mode, _, mErr := h.git.CheckAutoMerge(fsPath, iss.BranchName, contribBranch)
	if mErr == nil {
		_ = h.contributions.SetContributionMergeable(ctx, c.ID, mergeable, mode)
	}

	// Compute the initial review status: 'pending' when the branch has
	// required reviewers (path-matched from the host config), or 'approved'
	// straight away when it has none (e.g. an admin change with no matching
	// reviewer and the maintainer fallback being the author).
	h.recomputeContributionStatus(ctx, repo.ID, c)

	// Timeline event.
	evtPayload, _ := json.Marshal(domain.ContributionEventPayload{
		ContributionID: c.ID,
		AgentRole:      role,
		RefName:        c.RefName,
		HeadSHA:        headSHA,
		Title:          c.Title,
	})
	_, _ = h.issues.CreateAgentEvent(ctx, iss.ID, domain.EventContributionPushed, evtPayload, role)

	// Wake reviewers. Reuse commit.pushed semantics (the reviewer roster
	// subscribes to commit.pushed); the changed paths come from the real diff
	// so per-role paths / paths_ignore filters are accurate. CauseID is the
	// contribution head so re-deliveries of the same push dedupe.
	h.fireContributionPushed(ctx, repo, iss, c, headSHA, changedPaths)
}

// fireContributionPushed fans a commit.pushed trigger for a contribution push.
func (h *Handler) fireContributionPushed(ctx context.Context, repo *repodomain.Repo, iss *domain.Issue, c *domain.Contribution, headSHA string, changedPaths []string) {
	if h.spawner == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"contribution_id": c.ID,
		"ref_name":        c.RefName,
		"agent_role":      c.AgentRole,
		"head_sha":        headSHA,
	})
	actor := iss.AuthorID
	if _, err := h.spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
		Trigger:      agentsconfig.TriggerCommitPushed,
		CauseKind:    agentsessiondomain.CauseKindCommitPushed,
		CauseID:      "contrib-" + headSHA,
		RepoID:       repo.ID,
		IssueNumber:  int32(iss.Number),
		ActorID:      actor,
		ChangedPaths: changedPaths,
		Payload:      payload,
	}); err != nil {
		log.Printf("issue: fireContributionPushed repo=%d issue=%d ref=%s: %v", repo.ID, iss.Number, c.RefName, err)
	}
}

// requiredReviewers returns the reviewer role keys that must approve a
// contribution: path-matched from the host config's `reviewers:` block, minus
// the contribution author (a role cannot review its own branch). Returns nil
// when the host has no reviewers config or the yaml can't be loaded — the
// contribution then has no required reviewers and is approved without review.
func (h *Handler) requiredReviewers(ctx context.Context, repoID int64, c *domain.Contribution) []string {
	if h.spawner == nil {
		return nil
	}
	cfg, err := h.spawner.LoadHostConfig(ctx, repoID)
	if err != nil || cfg == nil {
		return nil
	}
	return excludeRole(cfg.RequiredReviewers(c.ChangedPaths), c.AgentRole)
}

// recomputeContributionStatus recomputes a non-terminal contribution's cached
// status from its required reviewers + current votes and persists it when it
// changed. Best-effort: a failure leaves the previous cached status in place.
func (h *Handler) recomputeContributionStatus(ctx context.Context, repoID int64, c *domain.Contribution) {
	if c.Status.Terminal() {
		return
	}
	events, err := h.issues.ListEvents(ctx, c.IssueID)
	if err != nil {
		return
	}
	rs := domain.ComputeContributionReviewStatus(c, h.requiredReviewers(ctx, repoID, c), events)
	if next := rs.ContributionStatus(); next != c.Status {
		_, _ = h.contributions.SetContributionStatus(ctx, c.ID, next)
	}
}

// excludeRole returns roles with every occurrence of role removed. A nil/empty
// role returns the input unchanged.
func excludeRole(roles []string, role string) []string {
	if role == "" || len(roles) == 0 {
		return roles
	}
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		if r != role {
			out = append(out, r)
		}
	}
	return out
}

// issueMergeBlock is the second-level (issue → base) gate: it returns a
// non-empty reason when the issue branch isn't ready to merge — some
// contribution is still open, or the issue branch carries no changes. Empty
// string means ready.
func (h *Handler) issueMergeBlock(ctx context.Context, fsPath string, iss *domain.Issue) string {
	contribs, err := h.contributions.ListContributions(ctx, iss.ID)
	if err != nil {
		return "cannot evaluate contributions: " + err.Error()
	}
	return domain.IssueMergeBlock(contribs, h.issueBranchAhead(fsPath, iss))
}

// issueBranchAhead reports whether the issue branch has commits its base does
// not — i.e. whether there is anything to merge into base.
func (h *Handler) issueBranchAhead(fsPath string, iss *domain.Issue) bool {
	head, err := h.git.ResolveCommit(fsPath, iss.BranchName)
	if err != nil || head == "" {
		return false
	}
	base, err := h.git.ResolveCommit(fsPath, iss.BaseBranch)
	if err != nil || base == "" {
		return true
	}
	if head == base {
		return false
	}
	isAnc, err := h.git.IsAncestor(fsPath, head, base)
	if err != nil {
		return true
	}
	return !isAnc
}

// contributionDiffStats derives changed paths + line counts from a real git
// diff (FileDiff per file), so stats are exact rather than hand-parsed.
func contributionDiffStats(diffs []*gitdomain.FileDiff) (changedPaths []string, files, additions, deletions int32) {
	// Non-nil so callers (the NOT NULL changed_paths column, the timeline
	// event payload, the reviewer trigger) never see a nil slice when the
	// diff is empty or couldn't be computed.
	changedPaths = []string{}
	seen := make(map[string]struct{}, len(diffs))
	for _, d := range diffs {
		p := d.NewPath
		if p == "" {
			p = d.OldPath
		}
		if p != "" {
			if _, dup := seen[p]; !dup {
				seen[p] = struct{}{}
				changedPaths = append(changedPaths, p)
			}
		}
		for _, line := range strings.Split(d.Patch, "\n") {
			switch {
			case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
				// file headers — ignore
			case strings.HasPrefix(line, "+"):
				additions++
			case strings.HasPrefix(line, "-"):
				deletions++
			}
		}
	}
	files = int32(len(changedPaths))
	return changedPaths, files, additions, deletions
}

// contributionIssueID resolves an issue number to its row id; returns 0 on
// failure (callers treat 0 as "no match" against the unique key).
func contributionIssueID(ctx context.Context, h *Handler, repoID, number int64) int64 {
	iss, err := h.issues.GetByNumber(ctx, repoID, number)
	if err != nil {
		return 0
	}
	return iss.ID
}

// isZeroOID reports whether sha is the git all-zero object id (a ref delete).
func isZeroOID(sha string) bool {
	if sha == "" {
		return true
	}
	for _, r := range sha {
		if r != '0' {
			return false
		}
	}
	return true
}
