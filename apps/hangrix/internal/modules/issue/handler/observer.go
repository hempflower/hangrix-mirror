package handler

import (
	"context"
	"fmt"
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
// the push. A push is rejected when any open issue's branch has diverged
// from its base (i.e. the base tip is not an ancestor of the new issue head).
//
// Non-issue branches are skipped. Already merged/closed issues are not
// checked — only open issues gate pushes.
func (o *PushObserver) PreReceive(ctx context.Context, repo *repodomain.Repo, fsPath string, refUpdates []repodomain.PushRefUpdate) error {
	for _, u := range refUpdates {
		refName := u.RefName
		// Strip "refs/heads/" prefix if present so we can match branch
		// names regardless of the ref namespace.
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

		// Use the new SHA from the push (not the current branch ref) so that
		// a rebased branch whose old tip had diverged is still accepted. The
		// branch ref on disk hasn't been updated yet at PreReceive time.
		isFF, mode, err := o.h.git.CheckFastForward(fsPath, iss.BaseBranch, u.NewSHA)
		if err != nil {
			return fmt.Errorf("check fast-forward for %s: %w", branch, err)
		}
		if !isFF {
			return fmt.Errorf(
				"branch has diverged from %s — rebase onto %s first (mode=%s)",
				iss.BaseBranch, iss.BaseBranch, mode,
			)
		}
	}
	return nil
}

// PostReceive walks every open issue in the repo and reconciles its on-disk
// branch tip. Repos with many open issues see an O(open_issues) walk per
// push — fine for M4 scale; we can add a "branches updated" filter once
// receive-pack instrumentation gives us that data.
func (o *PushObserver) PostReceive(ctx context.Context, repo *repodomain.Repo, fsPath string, pusher repodomain.Pusher) error {
	numbers, err := o.h.issues.ListOpenIssueNumbers(ctx, repo.ID)
	if err != nil {
		return err
	}
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
	return nil
}
