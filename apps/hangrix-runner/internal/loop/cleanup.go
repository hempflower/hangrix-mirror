package loop

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
)

// CleanupSweeper polls the platform for containers it should `docker rm`
// and removes them. The platform flags containers via three paths
// (issue-close archive, user-delete on the controller, 7-day idle reaper);
// the sweeper is agnostic to which path produced the flag — it just
// asks "what containers does the platform want gone?" and removes them.
//
// Decoupled from the task workers in loop.go because the cadence and
// concurrency story is different: cleanup is one-shot per (session,
// container) and inherently low-frequency, so a single goroutine doing
// sequential remove-then-ACK is plenty.
type CleanupSweeper struct {
	Client       *client.Client
	Orchestrator orchestrator.Orchestrator

	// Interval is the poll cadence. Defaults to 60s in Start when zero.
	// Operators rarely need to tune this — the platform's reaper runs
	// hourly and human-triggered cleanups (archive, delete) are bursty
	// rather than throughput-sensitive.
	Interval time.Duration
}

// Start blocks until ctx is cancelled. Runs an immediate sweep then
// ticks at Interval. Returns nil on clean cancel, otherwise the ctx
// error — matches the shape Loop.Run uses for its own goroutines.
func (s *CleanupSweeper) Start(ctx context.Context) error {
	interval := s.Interval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	s.sweepOnce(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil
			}
			return ctx.Err()
		case <-t.C:
			s.sweepOnce(ctx)
		}
	}
}

// sweepOnce drains the platform's cleanup queue in one pass. We loop
// until the platform returns an empty list so a backlog (e.g. after a
// large issue-close batch) clears in a single tick rather than waiting
// out Interval per batch. The platform caps batch size at 50 — bounded
// memory + bounded per-iteration work.
func (s *CleanupSweeper) sweepOnce(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		resp, err := s.Client.ListCleanupTasks(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("cleanup sweeper: list: %v", err)
			}
			return
		}
		if len(resp.Tasks) == 0 {
			return
		}
		for _, t := range resp.Tasks {
			s.handle(ctx, t)
		}
	}
}

// handle removes one container and ACKs the cleanup. RemoveContainer is
// idempotent (returns nil for already-gone ids) so we ACK on the happy
// path even if the container was reaped externally between the platform
// flagging it and our sweep. A remove error skips the ACK so the
// platform re-issues the task on the next poll — eventual consistency
// keeps the column from going stuck.
func (s *CleanupSweeper) handle(ctx context.Context, t client.CleanupTask) {
	if err := s.Orchestrator.RemoveContainer(ctx, t.ContainerID); err != nil {
		log.Printf("cleanup sweeper: session %d: remove %s: %v", t.SessionID, t.ContainerID, err)
		return
	}
	if err := s.Client.MarkCleanupDone(ctx, t.SessionID); err != nil {
		log.Printf("cleanup sweeper: session %d: ack: %v", t.SessionID, err)
		return
	}
	log.Printf("cleanup sweeper: removed container %s for session %d", t.ContainerID, t.SessionID)
}
