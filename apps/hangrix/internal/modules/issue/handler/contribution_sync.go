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
		return
	}
	number, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || number <= 0 {
		return
	}
	role := strings.SplitN(m[2], "/", 2)[0]
	if role == "" {
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
		return
	}
	// Only accept contributions against an open issue.
	if iss.State != domain.StateOpen {
		return
	}

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

// contributionDiffStats derives changed paths + line counts from a real git
// diff (FileDiff per file), so stats are exact rather than hand-parsed.
func contributionDiffStats(diffs []*gitdomain.FileDiff) (changedPaths []string, files, additions, deletions int32) {
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
