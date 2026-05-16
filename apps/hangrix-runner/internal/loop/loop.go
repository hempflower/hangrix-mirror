package loop

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"runtime"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
)

// Loop is the outer "what the runner does forever" object. It owns the
// heartbeat ticker and the task-poll loop; per-session work is delegated
// to SessionDriver. One Loop per process.
type Loop struct {
	Client       *client.Client
	Orchestrator orchestrator.Orchestrator
	// Bundles resolves an `<owner>/<name>@<sha>` task.AgentRepo pin into
	// a host directory the orchestrator bind-mounts at
	// /opt/hangrix/bundle:ro. Optional in tests that hand-craft Tasks
	// with empty AgentRepo (e.g. session_test.go); SessionDriver fails
	// the task if a Task carries AgentRepo and this is nil.
	Bundles BundleResolver

	AgentBinaryPath string
	WorkspaceRoot   string

	LLMEndpoint string
	MCPEndpoint string

	HeartbeatEvery time.Duration
}

// Run blocks until ctx is cancelled. Two concurrent loops:
//
//   - heartbeat ticker fires every HeartbeatEvery; reports capability snapshot.
//   - task poller calls /tasks (long-poll, 20s); on a hit, runs the session
//     synchronously before returning to poll again. M6c keeps concurrency
//     at 1 because the docker side-effects don't need to be racing yet;
//     M7a's dispatcher will widen this to runner.capabilities.parallelism.
func (l *Loop) Run(ctx context.Context) error {
	if l.HeartbeatEvery <= 0 {
		l.HeartbeatEvery = 20 * time.Second
	}

	// Heartbeat goroutine. Errors are logged but never fatal; a missed
	// heartbeat just means the platform's last_heartbeat_at lags.
	go l.heartbeatLoop(ctx)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		task, ok, err := l.Client.PollTasks(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			log.Printf("poll tasks: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if !ok {
			continue
		}
		log.Printf("claimed session %d (image=%s)", task.SessionID, task.AgentImage)
		drv := &SessionDriver{
			Client:          l.Client,
			Orchestrator:    l.Orchestrator,
			Bundles:         l.Bundles,
			AgentBinaryPath: l.AgentBinaryPath,
			WorkspaceRoot:   l.WorkspaceRoot,
			LLMEndpoint:     l.LLMEndpoint,
			MCPEndpoint:     l.MCPEndpoint,
		}
		exit, err := drv.Run(ctx, task)
		log.Printf("session %d finished: exit=%d err=%v", task.SessionID, exit, err)
	}
}

func (l *Loop) heartbeatLoop(ctx context.Context) {
	tick := time.NewTicker(l.HeartbeatEvery)
	defer tick.Stop()
	// Send an immediate heartbeat so the platform sees the runner live
	// straight after enroll, without waiting one tick.
	l.sendHeartbeat(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			l.sendHeartbeat(ctx)
		}
	}
}

func (l *Loop) sendHeartbeat(ctx context.Context) {
	caps := map[string]any{
		"os":   runtime.GOOS,
		"arch": runtime.GOARCH,
		"go":   runtime.Version(),
	}
	body, _ := json.Marshal(caps)
	if err := l.Client.Heartbeat(ctx, client.HeartbeatRequest{Capabilities: body}); err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Printf("heartbeat: %v", err)
		}
	}
}
