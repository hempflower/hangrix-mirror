package handler

import (
	"context"

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

func (o *PushObserver) PreReceive(ctx context.Context, repo *repodomain.Repo, fsPath string) error {
	return o.h.RefreshHook(ctx, repo, fsPath)
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
