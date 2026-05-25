package loop_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/loop"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
)

// TestLoopParallelismFansOut verifies Loop runs sessions in parallel
// when Parallelism > 1. The gating orchestrator below holds every
// session's Wait() open until the test sees N concurrent Start calls;
// if the loop were single-threaded only one would arrive and the
// "started" gate would never close, timing out the test.
func TestLoopParallelismFansOut(t *testing.T) {
	t.Parallel()
	const N = 3

	var (
		mu             sync.Mutex
		runningHits    atomic.Int32
		terminateHits  atomic.Int32
		issuedTasks    atomic.Int32
		seenSessionIDs []int64
	)

	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/runner/tasks":
			n := issuedTasks.Add(1)
			if n > N {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			sid := int64(1000 + n)
			mu.Lock()
			seenSessionIDs = append(seenSessionIDs, sid)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session_id":    sid,
				"agent_image":   "alpine:latest",
				"session_token": fmt.Sprintf("hgxs_AAAAAAAA_session%02d", n),
				"env":           map[string]string{},
			})
		case strings.HasSuffix(r.URL.Path, "/running"):
			runningHits.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/history"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"frame": json.RawMessage(`{"kind":"history","messages":[]}`),
			})
		case strings.HasSuffix(r.URL.Path, "/inputs"):
			// Block long-poll with a small sleep so we don't spin the
			// shipStdin goroutine; same shape as the real platform.
			time.Sleep(50 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{"frames": []json.RawMessage{}})
		case strings.HasSuffix(r.URL.Path, "/messages"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/terminate"):
			terminateHits.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/runner/heartbeat":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/runner/cleanup-tasks":
			// Cleanup sweeper poll — empty queue for this test.
			_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []any{}})
		case strings.HasSuffix(r.URL.Path, "/container"):
			// SetContainer ACK from the session driver after Start.
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected platform call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(platform.Close)

	orch := newGatingOrch(N)
	cli := client.New(platform.URL).WithAgentToken("hgxr_test_token")

	l := &loop.Loop{
		Client:          cli,
		Orchestrator:    orch,
		AgentBinaryPath: "/dev/null",
		WorkspaceRoot:   t.TempDir(),
		BaseURL:         "http://platform.test",
		HeartbeatEvery:  10 * time.Second, // suppress chatter during the test
		Parallelism:     N,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- l.Run(ctx) }()

	// Block here until N sessions have all entered Orchestrator.Start.
	// Single-worker behaviour (Parallelism<=1) would never close this.
	select {
	case <-orch.allStarted:
	case <-time.After(5 * time.Second):
		t.Fatalf("only %d/%d sessions reached Orchestrator.Start within 5s; loop is not running them in parallel",
			orch.startedCount(), N)
	}

	// All N runners hit /running before the wait gate releases — that's
	// the actual proof of concurrency from the platform's perspective.
	if got := runningHits.Load(); got != N {
		t.Fatalf("running hits at gate = %d, want %d", got, N)
	}

	// Release every in-flight session so they exit cleanly.
	orch.releaseAll(0)

	// All N must terminate; poll with a generous deadline so we don't
	// flake on slow CI hosts.
	deadline := time.Now().Add(5 * time.Second)
	for terminateHits.Load() < N && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := terminateHits.Load(); got != N {
		t.Fatalf("terminate hits = %d, want %d", got, N)
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Loop.Run returned %v, want nil or context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Loop.Run did not return after ctx cancel")
	}
}

// gatingOrch is a multi-session orchestrator double: every Start gets
// its own pipes + waitCh, so concurrent sessions don't share state. A
// barrier closes `allStarted` when N Start calls have arrived, which
// the test uses to verify parallelism.
type gatingOrch struct {
	target int

	mu         sync.Mutex
	handles    []*gatingHandle
	allStarted chan struct{}
}

func newGatingOrch(target int) *gatingOrch {
	return &gatingOrch{target: target, allStarted: make(chan struct{})}
}

func (g *gatingOrch) Start(ctx context.Context, _ orchestrator.Task) (orchestrator.Handle, error) {
	_ = ctx
	h := newGatingHandle()
	g.mu.Lock()
	g.handles = append(g.handles, h)
	if len(g.handles) == g.target {
		close(g.allStarted)
	}
	g.mu.Unlock()
	return h, nil
}

func (g *gatingOrch) RemoveContainer(context.Context, string) error { return nil }

func (g *gatingOrch) WorkflowContainer(_ context.Context, _ string, _ *orchestrator.BuildSpec, _ []string, _ string, _ map[string]string, _ []orchestrator.Volume) (string, error) {
	return "", errors.New("workflow container not supported in gatingOrch")
}

func (g *gatingOrch) Exec(_ context.Context, _, _ string, _ map[string]string, _ ...string) (orchestrator.ExecHandle, error) {
	return nil, errors.New("exec not supported in gatingOrch")
}

func (g *gatingOrch) startedCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.handles)
}

func (g *gatingOrch) releaseAll(code int) {
	g.mu.Lock()
	snapshot := append([]*gatingHandle(nil), g.handles...)
	g.mu.Unlock()
	for _, h := range snapshot {
		h.release(code)
	}
}

type gatingHandle struct {
	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	stderrR *io.PipeReader
	stderrW *io.PipeWriter

	once   sync.Once
	waitCh chan int
}

func newGatingHandle() *gatingHandle {
	h := &gatingHandle{waitCh: make(chan int, 1)}
	h.stdinR, h.stdinW = io.Pipe()
	h.stdoutR, h.stdoutW = io.Pipe()
	h.stderrR, h.stderrW = io.Pipe()
	return h
}

func (h *gatingHandle) Stdin() io.WriteCloser { return h.stdinW }
func (h *gatingHandle) Stdout() io.Reader     { return h.stdoutR }
func (h *gatingHandle) Stderr() io.Reader     { return h.stderrR }
func (h *gatingHandle) ContainerID() string   { return "" }

func (h *gatingHandle) Wait() (int, error) {
	code := <-h.waitCh
	return code, nil
}

func (h *gatingHandle) Stop(ctx context.Context) error {
	_ = ctx
	h.release(0)
	return nil
}

func (h *gatingHandle) release(code int) {
	h.once.Do(func() {
		h.waitCh <- code
		// Close the agent-side writers so the runner's shipStdout /
		// shipStderr goroutines drain and exit cleanly.
		_ = h.stdoutW.Close()
		_ = h.stderrW.Close()
		_ = h.stdinR.Close()
	})
}
