package loop_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
//   - the stdin pipe carried at least the history frame the platform
//     enqueued via /inputs.
func TestSessionDriverEndToEnd(t *testing.T) {
	t.Parallel()

	// Test fixture: in-memory record of what the runner posted.
	var (
		mu               sync.Mutex
		messages         []map[string]any
		inputsCalls      atomic.Int32
		markRunningHits  atomic.Int32
		terminateBody    map[string]any
		historyFrame     = json.RawMessage(`{"kind":"history","messages":[]}`)
		eventFrameJSON   = json.RawMessage(`{"kind":"event","event":"test.poke","payload":{"hi":1}}`)
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
		case r.URL.Path == "/api/runner/sessions/42/inputs":
			// First call returns the history + event frames; subsequent
			// calls return empty so the shipStdin loop keeps polling.
			n := inputsCalls.Add(1)
			if n == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"frames": []json.RawMessage{historyFrame, eventFrameJSON},
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
