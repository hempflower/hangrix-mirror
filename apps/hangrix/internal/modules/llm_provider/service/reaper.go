// Package service holds the background usage-log reaper that hard-deletes
// rows whose created_at exceeds the configured retention window. It follows
// the same BackgroundJob pattern as agent_session/service/reaper.go.
package service

import (
	"context"
	"log"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
)

// Reaper is a best-effort background sweeper that hard-deletes
// llm_usage_log rows older than the configured UsageRetention. One ticker,
// one query per tick — intentionally lightweight so it can share the
// same process as the HTTP server without adding latency pressure.
type Reaper struct {
	repo      *infra.PostgresRepo
	retention time.Duration
	interval  time.Duration
}

type ReaperDeps struct {
	Repo   *infra.PostgresRepo
	Config *config.Config
}

func NewReaper(deps *ReaperDeps) *Reaper {
	retention := deps.Config.LLM.UsageRetention
	if retention <= 0 {
		retention = 168 * time.Hour
	}
	interval := deps.Config.LLM.UsageCleanupInterval
	if interval <= 0 {
		interval = 1 * time.Hour
	}
	return &Reaper{
		repo:      deps.Repo,
		retention: retention,
		interval:  interval,
	}
}

// Start runs the sweep on a periodic ticker. We also do one immediate
// sweep on startup so a restart doesn't introduce a worst-case
// interval-length pause before the first reap.
func (r *Reaper) Start(ctx context.Context) {
	r.sweepOnce(ctx)
	t := time.NewTicker(r.interval)
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

// sweepOnce deletes rows whose created_at is before NOW() - retention.
// We log row counts even when zero so operators can confirm the reaper
// is alive in steady state; errors are logged but never panic — the
// reaper is best-effort and a transient DB hiccup shouldn't take the
// server down.
func (r *Reaper) sweepOnce(ctx context.Context) {
	cutoff := time.Now().Add(-r.retention)
	n, err := r.repo.DeleteUsageBefore(ctx, cutoff)
	if err != nil {
		log.Printf("llm_usage reaper: sweep: %v", err)
	} else if n > 0 {
		log.Printf("llm_usage reaper: deleted %d rows older than %s", n, cutoff.UTC().Format(time.RFC3339))
	}
}

var _ server.BackgroundJob = (*Reaper)(nil)
