package loop

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
)

// Loop is the outer "what the runner does forever" object. It owns the
// heartbeat ticker and the task-poll workers; per-session work is
// delegated to SessionDriver. One Loop per process.
type Loop struct {
	Client       *client.Client
	Orchestrator orchestrator.Orchestrator

	AgentBinaryPath string
	WorkspaceRoot   string

	BaseURL string

	HeartbeatEvery time.Duration

	// Parallelism is the max number of sessions this runner will drive
	// concurrently. <=0 falls back to 1 — defensive only; the CLI
	// config layer defaults to 16 long before reaching here. Each unit
	// runs an independent /tasks long-poller + session driver. The DB
	// claim is FOR UPDATE SKIP LOCKED so workers never collide on the
	// same row.
	Parallelism int
}

// Run blocks until ctx is cancelled. Internally it fans out into:
//
//   - one heartbeat goroutine (period = HeartbeatEvery).
//   - one cleanup-sweeper goroutine that polls the platform for
//     containers to `docker rm` (migration 00004 — archive / delete /
//     7-day idle).
//   - N task-worker goroutines, where N = max(Parallelism, 1). Each
//     worker independently long-polls /tasks; on a hit it claims the
//     row, drives the session synchronously, then loops back to poll.
//
// Return value mirrors the historical single-worker shape: nil when
// ctx is cancelled cleanly, otherwise the ctx error.
func (l *Loop) Run(ctx context.Context) error {
	if l.HeartbeatEvery <= 0 {
		l.HeartbeatEvery = 20 * time.Second
	}
	n := l.Parallelism
	if n < 1 {
		n = 1
	}

	// Heartbeat goroutine. Errors are logged but never fatal; a missed
	// heartbeat just means the platform's last_heartbeat_at lags.
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		l.heartbeatLoop(ctx, n)
	}()

	// Cleanup-sweeper goroutine. Independent of task workers because
	// cleanup is low-frequency and inherently sequential per container.
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		sw := &CleanupSweeper{Client: l.Client, Orchestrator: l.Orchestrator}
		if err := sw.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("cleanup sweeper exited: %v", err)
		}
	}()

	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			l.workerLoop(ctx, workerID)
		}(i)
	}
	wg.Wait()
	<-hbDone
	<-cleanupDone

	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil
	}
	return ctx.Err()
}

// workerLoop is one task-poller. Owns no state of its own — every
// session driver is constructed per-task so per-session resources
// (workdir, container handle) don't leak across iterations.
func (l *Loop) workerLoop(ctx context.Context, workerID int) {
	for {
		if ctx.Err() != nil {
			return
		}
		task, ok, err := l.Client.PollTasks(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Printf("worker %d: poll tasks: %v", workerID, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if !ok {
			continue
		}
		log.Printf("worker %d: claimed session %d (image=%s)", workerID, task.SessionID, task.AgentImage)
		drv := &SessionDriver{
			Client:          l.Client,
			Orchestrator:    l.Orchestrator,
			AgentBinaryPath: l.AgentBinaryPath,
			WorkspaceRoot:   l.WorkspaceRoot,
			BaseURL:         l.BaseURL,
		}
		exit, err := drv.Run(ctx, task)
		log.Printf("worker %d: session %d finished: exit=%d err=%v", workerID, task.SessionID, exit, err)
	}
}

func (l *Loop) heartbeatLoop(ctx context.Context, parallelism int) {
	tick := time.NewTicker(l.HeartbeatEvery)
	defer tick.Stop()
	// Send an immediate heartbeat so the platform sees the runner live
	// straight after enroll, without waiting one tick.
	l.sendHeartbeat(ctx, parallelism)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			l.sendHeartbeat(ctx, parallelism)
		}
	}
}

func (l *Loop) sendHeartbeat(ctx context.Context, parallelism int) {
	caps := map[string]any{
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"go":          runtime.Version(),
		"parallelism": parallelism,
	}
	body, _ := json.Marshal(caps)
	if err := l.Client.Heartbeat(ctx, client.HeartbeatRequest{Capabilities: body}); err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Printf("heartbeat: %v", err)
		}
	}
}
