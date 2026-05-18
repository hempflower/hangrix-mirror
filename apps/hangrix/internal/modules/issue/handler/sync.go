package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

// SyncIssueBranch reconciles an issue's recorded HeadSHA with the actual
// on-disk branch tip. If the branch advanced, a commit_pushed event is
// appended with the new commits. actorID may be 0 for syncs not tied to a
// user (the M5 agent will pass its own actor).
//
// Exported so the receive-pack hook chain (see RefreshAfterPush) can reuse
// the same logic without exposing the underlying stores.
func (h *Handler) SyncIssueBranch(ctx context.Context, repo *repodomain.Repo, fsPath string, iss *domain.Issue, actorID int64) error {
	headSHA, err := h.git.ResolveCommit(fsPath, iss.BranchName)
	if err != nil {
		// Branch doesn't exist on disk yet — treat as no-op. The store row
		// stays at HeadSHA="" which is the correct state.
		return nil
	}
	if headSHA == "" || headSHA == iss.HeadSHA {
		return nil
	}

	// Resolve old/new range. If iss.HeadSHA is empty we use the base branch
	// as the "before" baseline so the event lists every commit that's new
	// to the base branch — these are the commits actually being introduced.
	oldRef := iss.HeadSHA
	if oldRef == "" {
		oldRef = iss.BaseBranch
	}
	newCommits := collectNewCommits(h.git, fsPath, oldRef, headSHA)

	if err := h.issues.UpdateHeadSHA(ctx, iss.ID, headSHA); err != nil {
		return err
	}

	if len(newCommits) > 0 {
		payload := domain.CommitPushedPayload{
			OldSHA:  iss.HeadSHA,
			NewSHA:  headSHA,
			Commits: newCommits,
		}
		raw, _ := json.Marshal(payload)
		if _, err := h.issues.CreateEvent(ctx, iss.ID, domain.EventCommitPushed, raw, actorID); err != nil {
			return err
		}
		// Fan a commit.pushed trigger out to any subscribing roles
		// (typically reviewer / maintainer). Best-effort — a session-
		// spawn hiccup must not break the head-sha update or the
		// timeline event we just wrote.
		//
		// When the push observer invokes us with actorID=0 (no human
		// pushed; usually an agent acting via session token), fall
		// back to the issue's author so the new session row has a
		// valid created_by — agent_sessions.created_by FKs users(id)
		// and rejects 0.
		spawnActor := actorID
		if spawnActor == 0 {
			spawnActor = iss.AuthorID
		}
		h.fireCommitPushed(ctx, repo, fsPath, iss, oldRef, headSHA, raw, spawnActor)
	}
	return nil
}

// fireCommitPushed dispatches the commit.pushed trigger. CauseID is the
// new head sha so each push produces a distinct cause-key (subsequent
// pushes don't dedupe against earlier ones). Payload carries the
// commit list so the agent can read the changes without an issue_diff
// roundtrip — the data is already on the platform side.
//
// ChangedPaths is the union of file paths affected between oldRef and
// headSHA — collected once here so the spawner can match each
// subscribed role's commit.pushed paths / paths_ignore filter without
// re-shelling out to git per role.
func (h *Handler) fireCommitPushed(ctx context.Context, repo *repodomain.Repo, fsPath string, iss *domain.Issue, oldRef, headSHA string, commitsJSON []byte, actorID int64) {
	if h.spawner == nil {
		return
	}
	_, _ = h.spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
		Trigger:      agentsconfig.TriggerCommitPushed,
		CauseKind:    agentsessiondomain.CauseKindCommitPushed,
		CauseID:      headSHA,
		RepoID:       repo.ID,
		IssueNumber:  int32(iss.Number),
		ActorID:      actorID,
		ChangedPaths: collectChangedPaths(h.git, fsPath, oldRef, headSHA),
		Payload:      commitsJSON,
	})
}

// collectChangedPaths returns the deduplicated list of file paths
// (post-rename new paths; deletions surface their old path) that
// changed in the from..to range. Errors yield an empty list — a path
// filter that can't enumerate files falls back to "no match", which
// is the correct conservative behaviour (a role with paths set won't
// wake on a push we can't characterise).
func collectChangedPaths(g gitdomain.Git, fsPath, from, to string) []string {
	if from == "" || to == "" || from == to {
		return nil
	}
	diffs, err := g.DiffRefs(fsPath, from, to)
	if err != nil || len(diffs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(diffs))
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		p := d.NewPath
		if p == "" {
			p = d.OldPath
		}
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// collectNewCommits walks the new branch tip until it hits a commit that's
// reachable from the baseline. Best-effort: errors yield an empty slice
// rather than aborting the sync — losing a commit_pushed event is preferable
// to silently dropping the SHA update.
func collectNewCommits(g gitdomain.Git, fsPath, baseline, head string) []domain.CommitPushedSummary {
	// We use ListCommits with a small page-size + IsAncestor stop. Reusing
	// existing primitives keeps the git domain narrow.
	const cap = 50
	commits, err := g.ListCommits(fsPath, head, 0, cap)
	if err != nil {
		return nil
	}
	out := make([]domain.CommitPushedSummary, 0, len(commits))
	for _, c := range commits {
		if c.SHA == baseline {
			break
		}
		isAncestor, err := g.IsAncestor(fsPath, c.SHA, baseline)
		if err == nil && isAncestor {
			break
		}
		out = append(out, domain.CommitPushedSummary{
			SHA:         c.SHA,
			Message:     c.Message,
			AuthorName:  c.Author.Name,
			CommittedAt: c.CommittedAt,
		})
	}
	return out
}

// refreshIssueMode rewrites the receive-pack sidecar (hangrix-issue-mode)
// from the current set of open-issue numbers. Called after any state change
// that can change which branches a push should accept — issue creation,
// state transitions, merges.
func (h *Handler) refreshIssueMode(r *http.Request, rc *repoCtx) {
	if err := h.RefreshHook(r.Context(), rc.repo, rc.fsPath); err != nil {
		// Best-effort: a stale sidecar means a push might be denied that
		// "should" be accepted, or vice-versa. Surface via log only — we
		// don't want to fail the surrounding mutation.
		// (Standard library log is fine; the project's chi middleware
		// already wires request logging.)
		_ = err
	}
}

// RefreshHook regenerates the issue-mode sidecar so the pre-receive hook
// sees the current list of open issues. Public so the cross-module sync API
// (M4) and the agent hooks (M5) can call it without depending on the
// handler internals.
func (h *Handler) RefreshHook(ctx context.Context, repo *repodomain.Repo, fsPath string) error {
	openNumbers, err := h.issues.ListOpenIssueNumbers(ctx, repo.ID)
	if err != nil {
		return err
	}
	return h.storage.SyncIssueMode(fsPath, repo.DefaultBranch, openNumbers, true)
}
