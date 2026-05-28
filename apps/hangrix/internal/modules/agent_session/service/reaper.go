package service

import (
	"context"
	"log"
	"time"

	platformsettings "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
)

// Default thresholds used when the platform_settings store has no row
// for a given key (i.e. before the admin has ever configured it).
const (
	defaultIdleStopThreshold      = 1 * time.Hour
	defaultIdleRemovalThreshold   = 168 * time.Hour   // 7 days
	defaultAbandonedThreshold     = 720 * time.Hour   // 30 days
)

// Reaper is the platform-side background sweeper that drives the
// container-lifecycle gates:
//
//   - Idle-stop sweep: flags container_stop_pending for every live
//     container whose session has been idle longer than the configured
//     idle_stop_threshold. The runner then `docker stop`s it.
//
//   - Idle-removal sweep: flags container_cleanup_pending for every
//     live container whose session hasn't been touched within the
//     configured idle_removal_threshold. The runner then `docker rm`s.
//
//   - Abandoned sweep: clears container_id on rows that have been
//     flagged for cleanup for longer than the configured
//     abandoned_cleanup_threshold with no runner pickup.
//
// All three thresholds are read from platform_settings.Store on every
// tick so an admin PATCH takes effect within the next hour without a
// restart.
type Reaper struct {
	runner runnerdomain.Repo
	store  platformsettings.Store
}

type ReaperDeps struct {
	Runner runnerdomain.Repo
	Store  platformsettings.Store
}

func NewReaper(deps *ReaperDeps) *Reaper {
	return &Reaper{runner: deps.Runner, store: deps.Store}
}

// reaperInterval is the cadence between sweeps. Hourly is plenty:
// the gates are measured in hours/days, so even a 4-hour reaper would
// still hit each row well before the next gate boundary.
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

// sweepOnce runs all three sweeps back-to-back. Thresholds are read
// from the store on every tick; errors reading thresholds fall back to
// the compiled-in defaults so a transient DB hiccup doesn't skip a
// sweep.
func (r *Reaper) sweepOnce(ctx context.Context) {
	idleStop := r.readDuration(ctx, "lifecycle.idle_stop_threshold", defaultIdleStopThreshold)
	idleRemoval := r.readDuration(ctx, "lifecycle.idle_removal_threshold", defaultIdleRemovalThreshold)
	abandoned := r.readDuration(ctx, "lifecycle.abandoned_cleanup_threshold", defaultAbandonedThreshold)

	// 1. Idle-stop sweep: flag containers for docker stop.
	if n, err := r.runner.SweepIdleSessionContainersForStop(ctx, idleStop); err != nil {
		log.Printf("reaper: idle-stop sweep: %v", err)
	} else if n > 0 {
		log.Printf("reaper: flagged %d idle containers for stop (threshold=%s)", n, idleStop)
	}

	// 2. Idle-removal sweep: flag containers for docker rm.
	if n, err := r.runner.SweepIdleSessionContainers(ctx, idleRemoval); err != nil {
		log.Printf("reaper: idle-removal sweep: %v", err)
	} else if n > 0 {
		log.Printf("reaper: flagged %d idle containers for removal (threshold=%s)", n, idleRemoval)
	}

	// 3. Abandoned sweep: give up on containers flagged too long.
	if n, err := r.runner.SweepAbandonedSessionContainers(ctx, abandoned); err != nil {
		log.Printf("reaper: abandoned sweep: %v", err)
	} else if n > 0 {
		log.Printf("reaper: cleared container_id on %d abandoned sessions (no runner pickup after %s)", n, abandoned)
	}
}

// readDuration reads a threshold from the store, falling back to the
// default when the key is missing or unparseable.
func (r *Reaper) readDuration(ctx context.Context, key string, def time.Duration) time.Duration {
	d, err := r.store.GetDuration(ctx, key)
	if err != nil {
		log.Printf("reaper: reading %s: %v (falling back to %s)", key, err, def)
		return def
	}
	if d <= 0 {
		return def
	}
	return d
}

var _ server.BackgroundJob = (*Reaper)(nil)
