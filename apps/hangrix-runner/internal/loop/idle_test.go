package loop_test

import (
	"bufio"
	"context"
	"encoding/json"
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

// TestSessionIdleTimeoutSendsShutdown pins the headline runner-side
// change for long-lived containers: when the agent emits `idle` and no
// new event arrives within the idle timeout, the driver MUST send
// {"kind":"control","op":"shutdown"} on stdin so the agent reaps its
// background tasks and exits cleanly. Without this the container never
// retires and the runner's effective concurrency saturates.
//
// We drive the test with a sub-second idle timeout set on the
// SessionDriver itself so the suite isn't on the clock for a real
// 60-second wait. Per-driver timeouts (rather than a shared package
// var) keep parallel tests from racing on each other's values.
func TestSessionIdleTimeoutSendsShutdown(t *testing.T) {
	t.Parallel()

	platform, _ := newRecordingPlatform(t, 77, []json.RawMessage{
		json.RawMessage(`{"kind":"history","messages":[]}`),
	})

	fake := orchestrator.NewFake()
	cli := client.New(platform.URL).WithAgentToken("hgxr_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")

	// stdinObserver captures every line the runner writes to the
	// container's stdin. The agent fake echoes `idle` once it sees the
	// history frame, then exits on shutdown.
	stdinLines := make(chan string, 16)
	go observeStdin(fake.AgentStdin(), stdinLines)

	go func() {
		// Wait for history to land, then emit `idle` (no running jobs).
		// The runner should respond with control:shutdown after the
		// IdleTimeout configured on the driver below.
		for line := range stdinLines {
			if strings.Contains(line, `"kind":"history"`) {
				_, _ = fake.AgentStdout().Write([]byte(`{"kind":"idle","running_jobs":0}` + "\n"))
				continue
			}
			if strings.Contains(line, `"control"`) && strings.Contains(line, `"shutdown"`) {
				// Got the shutdown. The fake agent acks by exiting.
				fake.Exit(0)
				return
			}
		}
	}()

	drv := &loop.SessionDriver{
		Client:              cli,
		Orchestrator:        fake,
		AgentBinaryPath:     "/dev/null",
		WorkspaceRoot:       t.TempDir(),
		BaseURL:             "http://platform.test",
		IdleTimeout:         200 * time.Millisecond,
		IdleTimeoutWithJobs: 10 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	exit, err := drv.Run(ctx, &client.Task{
		SessionID:    77,
		AgentImage:   "alpine:latest",
		SessionToken: "hgxs_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		Env:          map[string]string{},
	})
	if err != nil {
		t.Fatalf("driver.Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit=%d, want 0", exit)
	}
	elapsed := time.Since(start)
	// We set idleTimeout=200ms; the run should complete in well under
	// 2 seconds. Anything more suggests the timer didn't fire and we
	// only exited because the agent's own goroutine errored / the test
	// hit the outer 5s deadline.
	if elapsed > 2*time.Second {
		t.Errorf("driver took %v; expected the idle timer (200ms) to drive a quick shutdown", elapsed)
	}
}

// TestSessionIdleTimeoutResetsOnNewEvent pins the negative case: when
// an event arrives BEFORE the idle timer fires, the timer must be
// cancelled. Otherwise back-to-back events would retire the container
// mid-flight just because the idle window happened to span the
// platform's event-shipping latency.
func TestSessionIdleTimeoutResetsOnNewEvent(t *testing.T) {
	t.Parallel()
	// 300ms idle window, plenty of margin so we can deliver a 2nd
	// event ~100ms after the first idle without racing the timer.
	const idleWindow = 300 * time.Millisecond

	// inputsCalls: first call returns history; second call (after a
	// gap) returns one event; subsequent calls return empty.
	var inputsCall atomic.Int32
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/running"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/inputs"):
			n := inputsCall.Add(1)
			switch n {
			case 1:
				_ = json.NewEncoder(w).Encode(map[string]any{
					"frames": []json.RawMessage{json.RawMessage(`{"kind":"history","messages":[]}`)},
				})
			case 2:
				// Pause briefly so the first idle has time to start
				// the retirement timer before we hand the agent its
				// second event.
				time.Sleep(120 * time.Millisecond)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"frames": []json.RawMessage{json.RawMessage(`{"kind":"event","event":"e","payload":{}}`)},
				})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"frames": []json.RawMessage{}})
			}
		case strings.HasSuffix(r.URL.Path, "/terminate"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/container"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(platform.Close)

	fake := orchestrator.NewFake()
	cli := client.New(platform.URL).WithAgentToken("hgxr_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")

	stdinLines := make(chan string, 16)
	go observeStdin(fake.AgentStdin(), stdinLines)

	// Track timing: when did the first idle fire? When did the event
	// land? If retirement reset works, the time between event arrival
	// and shutdown must equal (idle timeout + small jitter) starting
	// from the *second* idle — not from the first one.
	var (
		firstIdleAt    atomic.Int64
		eventSeenAt    atomic.Int64
		shutdownSeenAt atomic.Int64
	)

	go func() {
		for line := range stdinLines {
			switch {
			case strings.Contains(line, `"kind":"history"`):
				// Emit first idle.
				firstIdleAt.Store(time.Now().UnixNano())
				_, _ = fake.AgentStdout().Write([]byte(`{"kind":"idle","running_jobs":0}` + "\n"))
			case strings.Contains(line, `"kind":"event"`):
				eventSeenAt.Store(time.Now().UnixNano())
				// Emit a message (proves activity) then a second idle.
				_, _ = fake.AgentStdout().Write([]byte(`{"kind":"message","role":"assistant","content":"ok"}` + "\n"))
				_, _ = fake.AgentStdout().Write([]byte(`{"kind":"done","turn_id":"t1"}` + "\n"))
				_, _ = fake.AgentStdout().Write([]byte(`{"kind":"idle","running_jobs":0}` + "\n"))
			case strings.Contains(line, `"control"`) && strings.Contains(line, `"shutdown"`):
				shutdownSeenAt.Store(time.Now().UnixNano())
				fake.Exit(0)
				return
			}
		}
	}()

	drv := &loop.SessionDriver{
		Client:              cli,
		Orchestrator:        fake,
		AgentBinaryPath:     "/dev/null",
		WorkspaceRoot:       t.TempDir(),
		BaseURL:             "http://platform.test",
		IdleTimeout:         idleWindow,
		IdleTimeoutWithJobs: 10 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := drv.Run(ctx, &client.Task{
		SessionID:    78,
		AgentImage:   "alpine:latest",
		SessionToken: "hgxs_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		Env:          map[string]string{},
	}); err != nil {
		t.Fatalf("driver.Run: %v", err)
	}

	firstIdle := time.Unix(0, firstIdleAt.Load())
	eventSeen := time.Unix(0, eventSeenAt.Load())
	shutdown := time.Unix(0, shutdownSeenAt.Load())

	if eventSeen.IsZero() {
		t.Fatal("the agent never saw the event frame")
	}
	if shutdown.IsZero() {
		t.Fatal("the runner never sent shutdown")
	}
	// The first idle's retirement window would have fired ~300ms
	// after firstIdle. If retirement-reset worked, shutdown should be
	// AT LEAST 300ms after the *second* idle (which fires immediately
	// after eventSeen). So shutdown - eventSeen >= ~300ms.
	gapFromEvent := shutdown.Sub(eventSeen)
	if gapFromEvent < 200*time.Millisecond {
		t.Errorf("shutdown fired only %v after the event arrived — the first idle's timer wasn't cancelled by the event", gapFromEvent)
		t.Logf("firstIdle=%v event=%v shutdown=%v", firstIdle, eventSeen, shutdown)
	}
}

// TestSessionRunningJobsGetsLongerTimeout pins the running-jobs hint:
// when the agent reports background jobs alive on its idle frame, the
// runner must use the longer timeout band. Without this, a container
// babysitting a long test run would be retired prematurely.
func TestSessionRunningJobsGetsLongerTimeout(t *testing.T) {
	t.Parallel()
	// Short "no jobs" timeout, long "with jobs" timeout. We'll claim
	// running_jobs=1 and verify the run doesn't retire within the
	// short window. The "long" band is intentionally on the small side
	// (1s) because the test runs alongside other t.Parallel cases —
	// under CPU contention the watcher's timer can drift, so anything
	// bigger eats into the outer timeout budget and produces flaky
	// runs.
	const (
		idleShort = 100 * time.Millisecond
		idleLong  = 1 * time.Second
	)

	platform, _ := newRecordingPlatform(t, 79, []json.RawMessage{
		json.RawMessage(`{"kind":"history","messages":[]}`),
	})

	fake := orchestrator.NewFake()
	cli := client.New(platform.URL).WithAgentToken("hgxr_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")

	stdinLines := make(chan string, 16)
	go observeStdin(fake.AgentStdin(), stdinLines)

	go func() {
		for line := range stdinLines {
			if strings.Contains(line, `"kind":"history"`) {
				_, _ = fake.AgentStdout().Write([]byte(`{"kind":"idle","running_jobs":1}` + "\n"))
				continue
			}
			if strings.Contains(line, `"control"`) && strings.Contains(line, `"shutdown"`) {
				fake.Exit(0)
				return
			}
		}
	}()

	drv := &loop.SessionDriver{
		Client:              cli,
		Orchestrator:        fake,
		AgentBinaryPath:     "/dev/null",
		WorkspaceRoot:       t.TempDir(),
		BaseURL:             "http://platform.test",
		IdleTimeout:         idleShort,
		IdleTimeoutWithJobs: idleLong,
	}
	// Generous outer deadline relative to idleLong — the timer fires
	// after ~1s under no load, but parallel-load drift can push the
	// observable shutdown a couple of seconds later.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	if _, err := drv.Run(ctx, &client.Task{
		SessionID:    79,
		AgentImage:   "alpine:latest",
		SessionToken: "hgxs_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		Env:          map[string]string{},
	}); err != nil {
		t.Fatalf("driver.Run: %v", err)
	}
	elapsed := time.Since(start)

	// Short window is 100ms; long window is 1s. If running_jobs were
	// ignored we'd retire at ~100ms. We require >= 400ms to confirm
	// the long band kicked in. Upper bound is intentionally loose
	// (~12s) so the assertion remains stable when this test races
	// other t.Parallel cases for CPU.
	if elapsed < 400*time.Millisecond {
		t.Errorf("driver retired in %v despite running_jobs=1; expected the long timeout (~%v) to apply", elapsed, idleLong)
	}
	if elapsed > 12*time.Second {
		t.Errorf("driver hung for %v; idle timer never fired", elapsed)
	}
}

// observeStdin reads JSON-Lines off the fake orchestrator's stdin (i.e.
// whatever the runner writes to the agent's stdin) and forwards each
// line through `out`. Closing the stdin reader closes `out` so the
// consuming goroutine can exit.
func observeStdin(r io.Reader, out chan<- string) {
	defer close(out)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		out <- sc.Text()
	}
}

// newRecordingPlatform spins up a stub /api/runner/* server tailored
// to one session id. It returns the test server (caller cleans it up
// implicitly via t.Cleanup) and a snapshot of recorded /messages
// payloads (currently unused by the idle tests; kept for future
// assertions).
func newRecordingPlatform(t *testing.T, sessionID int64, frames []json.RawMessage) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var (
		mu       sync.Mutex
		messages []map[string]any
		called   atomic.Int32
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/runner/sessions/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/running"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			body, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			mu.Lock()
			messages = append(messages, m)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/inputs"):
			n := called.Add(1)
			if n == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"frames": frames})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"frames": []json.RawMessage{}})
		case strings.HasSuffix(r.URL.Path, "/terminate"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/container"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_ = sessionID
	return srv, &messages
}
