package service

import (
	"context"
	"log"
	"time"

	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
)

// Reaper is the platform-side background sweeper that drives the
// container-lifecycle gates added in migration 00004:
//
//   - Idle sweep (7 days): flips container_cleanup_pending = TRUE on
//     every live container whose session is non-running and hasn't been
//     touched in 7 days. The runner's cleanup poll then picks the row up
//     and `docker rm`s the container.
//
//   - Abandoned sweep (30 days): clears container_id on rows that have
//     been flagged for cleanup for over 30 days with no runner pickup
//     (typically because the owning runner is permanently offline).
//     This stops the flag from blocking other state forever; the
//     container is effectively orphaned on the host.
//
// Both gates are baked into the SQL queries (see queries.sql); the
// reaper is just the scheduler. One ticker, two queries per tick, no
// per-row work — this is intentionally lightweight so it can sit on the
// same process as the HTTP server.
type Reaper struct {
	runner runnerdomain.Repo
}

type ReaperDeps struct {
	Runner runnerdomain.Repo
}

func NewReaper(deps *ReaperDeps) *Reaper {
	return &Reaper{runner: deps.Runner}
}

// reaperInterval is the cadence between sweeps. Hourly is plenty:
// the gates are 7 days (idle) and 30 days (abandon), so even a 4-hour
// reaper would still hit each row well before the next gate boundary.
// One hour leaves room for an operator to nudge "give up after N days"
// shorter without re-tuning the interval.
const reaperInterval = 1 * time.Hour

// Start runs the sweep on a 1-hour ticker. We also do one immediate
// sweep on startup so a restart doesn't introduce a worst-case 1-hour
// pause before the first reap — matters most when an operator restarts
// the server right after archiving a large batch of issues.
func (r *Reaper) Start(ctx context.Context) {
	r.sweepOnce(ctx)
	t := time.NewTicker(reaperInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sweepOnce(ctx)
		}
	}
}

// sweepOnce runs both queries back-to-back. We log row counts even when
// zero so operators can confirm the reaper is alive in steady state;
// errors are logged but never panic — the reaper is best-effort and a
// transient DB hiccup shouldn't take the server down.
func (r *Reaper) sweepOnce(ctx context.Context) {
	if n, err := r.runner.SweepIdleSessionContainers(ctx); err != nil {
		log.Printf("reaper: idle sweep: %v", err)
	} else if n > 0 {
		log.Printf("reaper: flagged %d idle containers for cleanup", n)
	}
	if n, err := r.runner.SweepAbandonedSessionContainers(ctx); err != nil {
		log.Printf("reaper: abandoned sweep: %v", err)
	} else if n > 0 {
		log.Printf("reaper: cleared container_id on %d abandoned sessions (no runner pickup after 30 days)", n)
	}
}

var _ server.BackgroundJob = (*Reaper)(nil)
