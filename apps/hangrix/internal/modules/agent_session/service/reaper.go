package service

import (
	"context"
	"log"
	"time"

	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	settingsdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
)

// Reaper is the platform-side background sweeper that drives the
// container-lifecycle gates:
//
//   - Idle stop: flags container_stop_pending = TRUE on every live
//     container whose session is idle and hasn't been touched within
//     the configured lifecycle.idle_stop_threshold. Only fires when
//     running_jobs = 0 and container_cleanup_pending = FALSE.
//     The runner's stop-sweeper picks the row up and `docker stop`s.
//
//   - Idle removal: flags container_cleanup_pending = TRUE on live
//     containers whose session hasn't been touched within the
//     configured lifecycle.idle_removal_threshold. Runner does
//     `docker rm`.
//
//   - Abandoned sweep: clears container_id on rows flagged for
//     cleanup longer than lifecycle.abandoned_cleanup_threshold with
//     no runner pickup.
//
// All three thresholds are read from the platform_settings store on
// every sweep, so an admin PATCH takes effect without a restart.
type Reaper struct {
	runner   runnerdomain.Repo
	settings settingsdomain.Store
}

type ReaperDeps struct {
	Runner   runnerdomain.Repo
	Settings settingsdomain.Store
}

func NewReaper(deps *ReaperDeps) *Reaper {
	return &Reaper{runner: deps.Runner, settings: deps.Settings}
}

// reaperInterval is the cadence between sweeps. Hourly is plenty:
// the shortest gate is typically 1 hour, so an hourly tick adds ≤1h
// latency which is inside spec.
const reaperInterval = 1 * time.Hour

// Start runs the sweep on a 1-hour ticker. We also do one immediate
// sweep on startup so a restart doesn't introduce a worst-case 1-hour
// pause before the first reap.
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

// sweepOnce runs all three queries back-to-back. Thresholds are read
// fresh from the setting store each sweep so a PATCH takes effect on
// the next tick without restart.
func (r *Reaper) sweepOnce(ctx context.Context) {
	// ---- stop sweep ----
	stopT, err := r.settings.GetDuration(ctx, "lifecycle.idle_stop_threshold")
	if err != nil {
		log.Printf("reaper: read stop threshold: %v", err)
	} else if stopT > 0 {
		if n, err := r.runner.SweepIdleSessionContainersForStop(ctx, stopT); err != nil {
			log.Printf("reaper: stop sweep: %v", err)
		} else if n > 0 {
			log.Printf("reaper: flagged %d containers for stop", n)
		}
	}

	// ---- idle removal sweep ----
	removeT, err := r.settings.GetDuration(ctx, "lifecycle.idle_removal_threshold")
	if err != nil {
		log.Printf("reaper: read removal threshold: %v", err)
	} else if removeT > 0 {
		if n, err := r.runner.SweepIdleSessionContainers(ctx, removeT); err != nil {
			log.Printf("reaper: idle sweep: %v", err)
		} else if n > 0 {
			log.Printf("reaper: flagged %d idle containers for cleanup", n)
		}
	}

	// ---- abandoned sweep ----
	abandonT, err := r.settings.GetDuration(ctx, "lifecycle.abandoned_cleanup_threshold")
	if err != nil {
		log.Printf("reaper: read abandoned threshold: %v", err)
	} else {
		if n, err := r.runner.SweepAbandonedSessionContainers(ctx, abandonT); err != nil {
			log.Printf("reaper: abandoned sweep: %v", err)
		} else if n > 0 {
			log.Printf("reaper: cleared container_id on %d abandoned sessions (no runner pickup)", n)
		}
	}
}

var _ server.BackgroundJob = (*Reaper)(nil)
