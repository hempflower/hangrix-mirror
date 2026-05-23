package handler

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

// PushObserver implements repodomain.PushObserver. The methods route through
// the handler's existing helpers so we keep one source of truth for sidecar
// regeneration and head-SHA reconciliation.
//
// We expose the observer as a thin wrapper rather than directly satisfying
// the interface from *Handler so the ioc binding stays explicit — the
// container resolves []PushObserver, and only this wrapper appears in that
// slice.
type PushObserver struct {
	h *Handler
}

type PushObserverDeps struct {
	Handler *Handler
}

func NewPushObserver(deps *PushObserverDeps) *PushObserver {
	return &PushObserver{h: deps.Handler}
}

// PreReceive runs fast-forward checks on each `issue/<n>` branch touched by
// the push. A push is rejected when the update is not a fast-forward
// relative to the branch's existing tip (i.e. OldSHA is not an ancestor of
// NewSHA). This prevents force-pushes while allowing normal work when the
// base branch has moved forward.
//
// Non-issue branches are skipped. Already merged/closed issues are not
// checked — only open issues gate pushes.
//
// When the fast-forward status cannot be determined (mode="unknown"), e.g.
// because a ref hasn't been created yet or the old tip is unresolvable,
// the push is allowed through rather than rejected — the git subprocess is
// the authoritative source of truth for ref updates.
func (o *PushObserver) PreReceive(ctx context.Context, repo *repodomain.Repo, fsPath string, refUpdates []repodomain.PushRefUpdate) error {
	for _, u := range refUpdates {
		refName := u.RefName
		// Strip "refs/heads/" prefix if present.
		branch := strings.TrimPrefix(refName, "refs/heads/")

		// Only gate `issue/<n>` branches.
		if !strings.HasPrefix(branch, "issue/") {
			continue
		}

		numStr := strings.TrimPrefix(branch, "issue/")
		n, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil || n <= 0 {
			continue
		}

		iss, err := o.h.issues.GetByNumber(ctx, repo.ID, n)
		if err != nil {
			// Issue not found in DB — defer to git, no gate.
			continue
		}

		// Only gate open issues. Merged/closed issues are not checked.
		if iss.State != domain.StateOpen {
			continue
		}

		// Use the new SHA from the push. The pack objects have already
		// been extracted into the repo before PreReceive runs, so the
		// SHA is resolvable by go-git.
		isFF, mode, err := o.h.git.CheckFastForward(fsPath, u.OldSHA, u.NewSHA)
		if err != nil {
			return fmt.Errorf("check fast-forward for %s: %w", branch, err)
		}
		if isFF {
			continue
		}
		// mode="unknown" means we couldn't determine ancestry (e.g.
		// unresolvable ref, branch not yet created). Let git be the
		// authority — don't reject the push.
		if mode == "unknown" {
			continue
		}
		// mode="diverged" means the branch has genuinely diverged from
		// its base. Reject with a sentinel so the handler can map to 409.
		return fmt.Errorf("%w: push to %s is not fast-forward",
			repodomain.ErrBranchDiverged, branch)
	}
	return nil
}

// PostReceive does two things:
//
//  1. For every pushed ref in a per-issue contribution namespace
//     (refs/heads/issue-<N>/<role>), upserts the contribution record from the
//     real git diff and wakes reviewers. See SyncContribution.
//  2. Walks every open issue in the repo and reconciles its on-disk branch
//     tip (commit_pushed events on `issue/<n>` branches).
//
// Contribution sync runs FIRST: it is the targeted work for the refs that were
// actually pushed, and must not be starved (or skipped on a panic / consumed
// 10s timeout) by the O(open_issues) reconciliation walk that follows. Repos
// with many open issues see that walk per push — fine for current scale.
func (o *PushObserver) PostReceive(ctx context.Context, repo *repodomain.Repo, fsPath string, pusher repodomain.Pusher, refUpdates []repodomain.PushRefUpdate) ([]repodomain.PostReceiveContrib, error) {
	// Contribution namespace refs first. Best-effort per ref.
	refNames := make([]string, len(refUpdates))
	for i, u := range refUpdates {
		refNames[i] = u.RefName
	}
	log.Printf("issue: PostReceive repo=%d refs=%v", repo.ID, refNames)
	var contribs []repodomain.PostReceiveContrib
	for _, u := range refUpdates {
		if c := o.h.SyncContribution(ctx, repo, fsPath, u); c != nil {
			contribs = append(contribs, *c)
		}
	}

	numbers, err := o.h.issues.ListOpenIssueNumbers(ctx, repo.ID)
	if err == nil {
		for _, n := range numbers {
			iss, err := o.h.issues.GetByNumber(ctx, repo.ID, n)
			if err != nil {
				continue
			}
			if err := o.h.SyncIssueBranch(ctx, repo, fsPath, iss, pusher.UserID, pusher.AgentRole); err != nil {
				// Best-effort per-issue: keep going so one bad branch doesn't
				// stall the rest.
				continue
			}
		}
	}
	return contribs, nil
}
