package handler

import (
	"context"
	"encoding/json"
	"log"

	"github.com/hangrix/hangrix/pkg/actor"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

// SyncIssueBranch reconciles an issue's recorded HeadSHA with the actual
// on-disk branch tip. If the branch advanced, a commit_pushed event is
// appended with the new commits.
//
// Attribution: exactly one of actorID / agentRole identifies the pusher.
// actorID > 0 attributes the event to a human user; agentRole != ""
// attributes it to a host yaml role (matches how the same agent's comments
// and review_vote events are stored, so the timeline renders one
// consistent identity across event kinds). Both zero is allowed for
// background syncs not tied to anyone — the timeline will show a dash.
//
// Exported so the receive-pack hook chain (see RefreshAfterPush) can reuse
// the same logic without exposing the underlying stores.
func (h *Handler) SyncIssueBranch(ctx context.Context, repo *repodomain.Repo, fsPath string, iss *domain.Issue, actorID int64, agentRole string) error {
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
		if agentRole != "" {
			if _, err := h.issues.CreateAgentEvent(ctx, iss.ID, domain.EventCommitPushed, raw, agentRole); err != nil {
				return err
			}
		} else {
			if _, err := h.issues.CreateEvent(ctx, iss.ID, domain.EventCommitPushed, raw, actorID, ""); err != nil {
				return err
			}
		}
		// Fan a commit.pushed trigger out to any subscribing roles
		// (typically reviewer / maintainer). Best-effort — a session-
		// spawn hiccup must not break the head-sha update or the
		// timeline event we just wrote.
		//
		// Resolve the triggering actor via the actor module's Resolver.
		// The old 3-way fallback (actorID → iss.AuthorID → repo.OwnerID)
		// is replaced with a single resolution path. When the resolver
		// is nil (tests), we fall back to the legacy actorID.
		triggerActor := h.resolveCommitPushedActor(ctx, actorID, agentRole, repo, iss)
		h.fireCommitPushed(ctx, repo, fsPath, iss, oldRef, headSHA, raw, actorID, triggerActor)
	}
	return nil
}

// fireCommitPushed dispatches the commit.pushed trigger. CauseID is the
// new head sha so each push produces a distinct cause-key (subsequent
// pushes don't dedupe against earlier ones). Payload carries the
// commit list so the agent can read the changes without a roundtrip —
// the data is already on the platform side.
//
// ChangedPaths is the union of file paths affected between oldRef and
// headSHA — collected once here so the spawner can match each
// subscribed role's commit.pushed paths / paths_ignore filter without
// re-shelling out to git per role.
func (h *Handler) fireCommitPushed(ctx context.Context, repo *repodomain.Repo, fsPath string, iss *domain.Issue, oldRef, headSHA string, commitsJSON []byte, actorID int64, triggerActor *actor.Ref) {
	if h.spawner == nil {
		return
	}
	if _, err := h.spawner.OnTrigger(ctx, agentsessiondomain.TriggerInput{
		Trigger:      agentsconfig.TriggerCommitPushed,
		CauseKind:    agentsessiondomain.CauseKindCommitPushed,
		CauseID:      headSHA,
		RepoID:       repo.ID,
		IssueNumber:  int32(iss.Number),
		Actor:        triggerActor,
		ActorID:      actorID,
		ChangedPaths: collectChangedPaths(h.git, fsPath, oldRef, headSHA),
		Payload:      commitsJSON,
	}); err != nil {
		// Same rationale as fireIssueOpened: don't drop a whole-config
		// failure (typically a malformed `.hangrix/agents.yml`) on the
		// floor — surface it so the operator can correlate against the
		// push that just landed.
		log.Printf("issue: fireCommitPushed repo=%d issue=%d head=%s: %v", repo.ID, iss.Number, headSHA, err)
	}
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


// resolveCommitPushedActor resolves the pusher's actor identity for the
// commit.pushed trigger. When the actorResolver is available, it resolves
// from actorID (user push) or agentRole (agent push); otherwise returns nil.
func (h *Handler) resolveCommitPushedActor(ctx context.Context, actorID int64, agentRole string, repo *repodomain.Repo, iss *domain.Issue) *actor.Ref {
	if h.actorResolver == nil {
		return nil
	}
	if actorID > 0 {
		resolved, err := h.actorResolver.From(ctx, actor.UserRef(actorID, ""))
		if err != nil {
			log.Printf("issue: resolve commit_pushed actor user:%d: %v", actorID, err)
			return nil
		}
		return resolved
	}
	if agentRole != "" {
		resolved, err := h.actorResolver.From(ctx, actor.AgentRef(agentRole))
		if err != nil {
			log.Printf("issue: resolve commit_pushed actor agent:%s: %v", agentRole, err)
			return nil
		}
		return resolved
	}
	// No direct pusher — fall back to issue author.
	if iss.AuthorID > 0 {
		resolved, err := h.actorResolver.From(ctx, actor.UserRef(iss.AuthorID, ""))
		if err != nil {
			log.Printf("issue: resolve commit_pushed actor issue_author:%d: %v", iss.AuthorID, err)
			return nil
		}
		return resolved
	}
	// Last resort: system actor.
	if repo.OwnerKind == repodomain.OwnerKindUser && repo.OwnerID > 0 {
		resolved, err := h.actorResolver.From(ctx, actor.UserRef(repo.OwnerID, ""))
		if err != nil {
			log.Printf("issue: resolve commit_pushed actor repo_owner:%d: %v", repo.OwnerID, err)
			return nil
		}
		return resolved
	}
	return nil
}