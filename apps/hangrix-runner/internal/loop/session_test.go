package loop_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/loop"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
)

// TestSessionDriverEndToEnd is the M6c runner-side smoke. It exercises
// the full session loop without docker: the orchestrator is the in-
// memory FakeOrchestrator, the platform is a stub httptest.Server, and
// the "agent" is a goroutine that emits a status + tool_call + message
// + done sequence after consuming the seed history frame.
//
// Assertions:
//   - server.markRunning was called exactly once for the session.
//   - all four outbound IPC frames the fake agent emitted ended up
//     as /messages POSTs on the platform.
//   - server.terminate was called with status=succeeded and exit 0.
//   - the driver fetched the history frame from GET /history once and
//     wrote it onto the agent's stdin before the /inputs shipper ran.
func TestSessionDriverEndToEnd(t *testing.T) {
	t.Parallel()

	// Test fixture: in-memory record of what the runner posted.
	var (
		mu              sync.Mutex
		messages        []map[string]any
		inputsCalls     atomic.Int32
		historyCalls    atomic.Int32
		markRunningHits atomic.Int32
		pingCalls       atomic.Int32
		terminateBody   map[string]any
		eventFrameJSON  = json.RawMessage(`{"kind":"event","event":"test.poke","payload":{"hi":1}}`)
	)

	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/runner/sessions/42/running":
			markRunningHits.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/runner/sessions/42/messages":
			body, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			mu.Lock()
			messages = append(messages, m)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/runner/sessions/42/history":
			historyCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"frame": json.RawMessage(`{"kind":"history","messages":[]}`),
			})
		case r.URL.Path == "/api/runner/sessions/42/inputs":
			// First call returns the event frame; subsequent calls return
			// empty so the shipStdin loop keeps polling. History is no
			// longer on this queue — it's served via /history above.
			n := inputsCalls.Add(1)
			if n == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"frames": []json.RawMessage{eventFrameJSON},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"frames": []json.RawMessage{}})
		case r.URL.Path == "/api/runner/sessions/42/terminate":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			_ = json.Unmarshal(body, &terminateBody)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/runner/sessions/42/ping":
			pingCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/runner/sessions/42/container":
			// SetContainer ACK from the session driver — the fake
			// orchestrator returns "fake-container-42" via Handle.ContainerID,
			// the driver posts it back, this endpoint records receipt.
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected platform call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(platform.Close)

	cli := client.New(platform.URL).WithAgentToken("hgxr_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	fake := orchestrator.NewFake()

	// Fake agent goroutine. Drains stdin (we don't assert exact bytes
	// here; the inputs-call counter proves they were shipped), then
	// emits four outbound frames the runner will forward.
	go func() {
		// Drain stdin until first frame arrives so the test moves
		// forward deterministically.
		buf := make([]byte, 4096)
		_, _ = fake.AgentStdin().Read(buf)
		_, _ = fake.AgentStdout().Write([]byte(`{"kind":"status","phase":"ready"}` + "\n"))
		_, _ = fake.AgentStdout().Write([]byte(`{"kind":"tool_call","tool_call_id":"tc1","name":"read","args":{"path":"/tmp/x"},"result":{"content":"hi"}}` + "\n"))
		_, _ = fake.AgentStdout().Write([]byte(`{"kind":"message","role":"assistant","content":"all done"}` + "\n"))
		_, _ = fake.AgentStdout().Write([]byte(`{"kind":"done","turn_id":"turn_abc"}` + "\n"))
		// Tiny grace so the runner pumps the frames before we close.
		time.Sleep(150 * time.Millisecond)
		fake.Exit(0)
	}()

	drv := &loop.SessionDriver{
		Client:          cli,
		Orchestrator:    fake,
		AgentBinaryPath: "/dev/null", // fake doesn't actually need this
		WorkspaceRoot:   t.TempDir(),
		BaseURL:         "http://platform.test",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	task := &client.Task{
		SessionID:    42,
		AgentImage:   "alpine:latest",
		SessionToken: "hgxs_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		Env:          map[string]string{},
	}
	exit, err := drv.Run(ctx, task)
	if err != nil {
		t.Fatalf("driver.Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit=%d, want 0", exit)
	}

	if got := markRunningHits.Load(); got != 1 {
		t.Errorf("markRunning hits=%d, want 1", got)
	}
	if got := historyCalls.Load(); got != 1 {
		t.Errorf("history fetch hits=%d, want 1", got)
	}
	// 4 non-idle frames (status, tool_call, message, done) ⇒ 4 ping calls.
	if got := pingCalls.Load(); got != 4 {
		t.Errorf("ping calls=%d, want 4 (one per non-idle frame)", got)
	}

	mu.Lock()
	defer mu.Unlock()

	// We should have at least the four frames the fake agent emitted.
	// Stderr / shipStderr can add more 'log' frames but never fewer.
	kinds := map[string]int{}
	for _, m := range messages {
		k, _ := m["kind"].(string)
		kinds[k]++
	}
	for _, want := range []string{"status", "tool_call", "message", "done"} {
		if kinds[want] < 1 {
			t.Errorf("missing forwarded frame kind=%s; got %#v", want, kinds)
		}
	}

	// Clean exit maps to 'idle', not 'succeeded': the per-issue per-
	// role session row stays reusable for the next trigger. Failed-
	// exit handling still routes to 'failed' (covered separately by
	// the agent-loop tests).
	if terminateBody["status"] != "idle" {
		t.Errorf("terminate status=%v, want idle", terminateBody["status"])
	}
}

// TestSessionDriverSkipsCloneWhenReusingContainer is the regression for
// the runc "current working directory is outside of container mount
// namespace root" failure: when a session is re-triggered, the runner
// must NOT re-clone the workdir, because the existing long-lived
// container has /workspace bind-mounted to that dir's inode. Wiping +
// re-cloning gives the dir a fresh inode and breaks the in-container
// mount on the next `docker exec`.
//
// We detect "clone happened" by intercepting the platform's /git/ path
// on the stub server: cloneRepo would HTTP-GET that path; if the gate
// works, the path is never touched. We also assert the orchestrator
// received HostWorkdir = repoCheckout (so a fallback container rebuild
// would still bind-mount the right subdir).
func TestSessionDriverSkipsCloneWhenReusingContainer(t *testing.T) {
	t.Parallel()

	var cloneHits atomic.Int32
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/git/"):
			// cloneRepo would land here. With ContainerID set, we expect
			// the runner to skip the clone entirely.
			cloneHits.Add(1)
			http.Error(w, "clone path should not be hit", http.StatusInternalServerError)
		case r.URL.Path == "/api/runner/sessions/77/running",
			r.URL.Path == "/api/runner/sessions/77/messages",
			r.URL.Path == "/api/runner/sessions/77/terminate",
			r.URL.Path == "/api/runner/sessions/77/container":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/runner/sessions/77/history":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"frame": json.RawMessage(`{"kind":"history","messages":[]}`),
			})
		case r.URL.Path == "/api/runner/sessions/77/inputs":
			_ = json.NewEncoder(w).Encode(map[string]any{"frames": []json.RawMessage{}})
		default:
			t.Errorf("unexpected platform call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(platform.Close)

	cli := client.New(platform.URL).WithAgentToken("hgxr_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	fake := orchestrator.NewFake()

	go func() {
		// Wait until the driver has written the history frame (the first
		// stdin byte) before tearing down the fake agent. Reading from
		// stdin blocks until the driver writes; exiting too early would
		// race the stdin write and surface a "closed pipe" error.
		buf := make([]byte, 4096)
		_, _ = fake.AgentStdin().Read(buf)
		fake.Exit(0)
	}()

	wsroot := t.TempDir()
	drv := &loop.SessionDriver{
		Client:          cli,
		Orchestrator:    fake,
		AgentBinaryPath: "/dev/null",
		WorkspaceRoot:   wsroot,
		BaseURL:         platform.URL,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	task := &client.Task{
		SessionID:    77,
		AgentImage:   "alpine:latest",
		SessionToken: "hgxs_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		ContainerID:  "existing-container-77",
		Env: map[string]string{
			"HANGRIX_HOST_OWNER": "alice",
			"HANGRIX_HOST_NAME":  "myproject",
		},
	}
	if _, err := drv.Run(ctx, task); err != nil {
		t.Fatalf("driver.Run: %v", err)
	}

	if got := cloneHits.Load(); got != 0 {
		t.Errorf("clone HTTP hits=%d, want 0 (reused container must not re-clone)", got)
	}

	wantWorkdir := filepath.Join(wsroot, "session-77", "repo")
	if got := fake.LastTask().HostWorkdir; got != wantWorkdir {
		t.Errorf("orchestrator HostWorkdir = %q, want %q", got, wantWorkdir)
	}
	if got := fake.LastTask().ContainerID; got != "existing-container-77" {
		t.Errorf("orchestrator ContainerID = %q, want existing-container-77", got)
	}
}

// TestSessionDriverForwardsVolumes verifies that client.Task.Volumes are
// mapped through to orchestrator.Task.Volumes inside SessionDriver.Run.
// This is the integration-side regression test for the volume plumbing:
// the field must survive deserialisation (client.Task), the session
// driver's mapping (mapVolumes), and land on the orchestrator task the
// fake orchestrator records.
func TestSessionDriverForwardsVolumes(t *testing.T) {
	t.Parallel()

	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/runner/sessions/99/running",
			r.URL.Path == "/api/runner/sessions/99/messages",
			r.URL.Path == "/api/runner/sessions/99/terminate",
			r.URL.Path == "/api/runner/sessions/99/container":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/runner/sessions/99/history":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"frame": json.RawMessage(`{"kind":"history","messages":[]}`),
			})
		case r.URL.Path == "/api/runner/sessions/99/inputs":
			_ = json.NewEncoder(w).Encode(map[string]any{"frames": []json.RawMessage{}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(platform.Close)

	cli := client.New(platform.URL).WithAgentToken("hgxr_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	fake := orchestrator.NewFake()

	go func() {
		buf := make([]byte, 4096)
		_, _ = fake.AgentStdin().Read(buf)
		fake.Exit(0)
	}()

	drv := &loop.SessionDriver{
		Client:          cli,
		Orchestrator:    fake,
		AgentBinaryPath: "/dev/null",
		WorkspaceRoot:   t.TempDir(),
		BaseURL:         "http://platform.test",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	task := &client.Task{
		SessionID:    99,
		AgentImage:   "alpine:latest",
		SessionToken: "hgxs_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		Env:          map[string]string{},
		Volumes: []client.Volume{
			{Name: "npm-cache", Mount: "/root/.npm"},
			{Name: "go-build-cache", Mount: "/root/.cache/go-build"},
		},
	}
	if _, err := drv.Run(ctx, task); err != nil {
		t.Fatalf("driver.Run: %v", err)
	}

	got := fake.LastTask().Volumes
	if len(got) != 2 {
		t.Fatalf("orchestrator task volumes len = %d, want 2", len(got))
	}
	if got[0].Name != "npm-cache" || got[0].Mount != "/root/.npm" {
		t.Errorf("volume 0 = %+v, want {npm-cache /root/.npm}", got[0])
	}
	if got[1].Name != "go-build-cache" || got[1].Mount != "/root/.cache/go-build" {
		t.Errorf("volume 1 = %+v, want {go-build-cache /root/.cache/go-build}", got[1])
	}
}

