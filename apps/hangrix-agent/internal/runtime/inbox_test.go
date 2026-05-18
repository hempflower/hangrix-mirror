package runtime_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/ipc"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/runtime"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
)

// fakeBashLifecycle is a hand-rolled stand-in for local.BashLifecycle.
// The runtime tests don't want to spawn real bash subprocesses; instead
// they want full control over when notifications fire and how many
// jobs are "running" at a given moment. The agent loop calls only the
// three interface methods, so an in-memory impl is enough.
type fakeBashLifecycle struct {
	notifications chan string
	running       atomic.Int32
	cleanupCount  atomic.Int32
}

func newFakeBash() *fakeBashLifecycle {
	return &fakeBashLifecycle{notifications: make(chan string, 16)}
}

func (f *fakeBashLifecycle) NotificationCh() <-chan string { return f.notifications }
func (f *fakeBashLifecycle) HasRunningJobs() int            { return int(f.running.Load()) }
func (f *fakeBashLifecycle) Cleanup(ctx context.Context)    { f.cleanupCount.Add(1) }

// TestLoopEmitsIdleAfterEvent pins the long-lived-agent contract: after
// the assistant finishes a turn (no more tool calls), the loop must
// emit an `idle` outbound frame so the runner knows the container is
// reusable for the next queued event. Regress this and the runner can
// never tell when it's safe to retire a container, so every event has
// to spawn a fresh one.
func TestLoopEmitsIdleAfterEvent(t *testing.T) {
	t.Parallel()

	// A trivial scripted LLM: every call returns a final assistant
	// message with no tool calls. So one event = one round = idle.
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id": "resp_ok",
			"output": []map[string]any{
				{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": "ack"}}},
			},
			"usage": map[string]any{},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil)

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	defer stdinR.Close()
	defer stdoutR.Close()

	loop := runtime.NewLoop(
		ipc.NewReader(stdinR),
		ipc.NewWriter(stdoutW),
		llmClient,
		"gpt-4o-mini",
		registry,
		"system prompt for test",
		bundle.Bash,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"x","payload":{}}` + "\n"))
		time.Sleep(200 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	frames, err := drainFrames(stdoutR)
	if err != nil {
		t.Fatalf("drain frames: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop: %v", err)
	}

	// The sequence we care about: done(event) -> idle(running_jobs=0).
	// We don't pin the absolute position of idle (status frames bracket
	// the work), just that:
	//   * exactly one done frame appears for the event
	//   * an idle frame appears AFTER it
	//   * the idle frame reports 0 running jobs (no bash work fired)
	var idleIdx, doneIdx int = -1, -1
	for i, f := range frames {
		switch f.Kind {
		case "done":
			if doneIdx != -1 {
				t.Errorf("expected exactly one done frame; saw two")
			}
			doneIdx = i
		case "idle":
			if idleIdx != -1 {
				t.Errorf("expected exactly one idle frame; saw two")
			}
			idleIdx = i
			if f.RunningJobs != 0 {
				t.Errorf("idle.running_jobs = %d, want 0", f.RunningJobs)
			}
		}
	}
	if doneIdx == -1 {
		t.Fatal("no done frame seen")
	}
	if idleIdx == -1 {
		t.Fatal("no idle frame seen; runner has no signal to retire the container")
	}
	if idleIdx <= doneIdx {
		t.Errorf("idle (idx=%d) should come after done (idx=%d)", idleIdx, doneIdx)
	}
}

// TestLoopProcessesMultipleEvents pins the headline lifecycle change:
// one container can drain multiple events before exiting. We send two
// events, then shutdown, and require:
//   - two distinct `done` frames (one per event)
//   - two `idle` frames (one after each event)
//   - the loop did NOT return after the first event, but kept reading
//     stdin until shutdown
func TestLoopProcessesMultipleEvents(t *testing.T) {
	t.Parallel()

	var llmCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "r",
			"output": []map[string]any{
				{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": "ok"}}},
			},
			"usage": map[string]any{},
		})
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil)

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	defer stdinR.Close()
	defer stdoutR.Close()

	loop := runtime.NewLoop(
		ipc.NewReader(stdinR),
		ipc.NewWriter(stdoutW),
		llmClient,
		"gpt-4o-mini",
		registry,
		"system prompt",
		bundle.Bash,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"first","payload":{}}` + "\n"))
		// Wait long enough for the first event's done/idle to flush so
		// the second event genuinely arrives "after idle", not piggy-
		// backed inside the first event's pendingFrames.
		time.Sleep(300 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"second","payload":{}}` + "\n"))
		time.Sleep(300 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	frames, err := drainFrames(stdoutR)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop: %v", err)
	}

	var doneCount, idleCount int
	for _, f := range frames {
		switch f.Kind {
		case "done":
			doneCount++
		case "idle":
			idleCount++
		}
	}
	if doneCount != 2 {
		t.Errorf("expected 2 done frames (one per event); got %d", doneCount)
	}
	if idleCount != 2 {
		t.Errorf("expected 2 idle frames (one after each event); got %d", idleCount)
	}
	if got := llmCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls; got %d", got)
	}
}

// TestLoopShutdownInvokesBashCleanup pins the cleanup hook. When the
// runner sends control:shutdown and there are still-running background
// bash tasks, the loop MUST call BashLifecycle.Cleanup with a bounded
// context before returning — otherwise jobs leak past the agent exit
// and live on as orphaned children inside the container until teardown
// (and lose any chance of an orderly SIGTERM).
func TestLoopShutdownInvokesBashCleanup(t *testing.T) {
	t.Parallel()

	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "r",
			"output": []map[string]any{
				{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": "ok"}}},
			},
			"usage": map[string]any{},
		})
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	fake := newFakeBash()
	fake.running.Store(2) // pretend two bash jobs are still alive

	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil)

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	defer stdinR.Close()
	defer stdoutR.Close()

	loop := runtime.NewLoop(
		ipc.NewReader(stdinR),
		ipc.NewWriter(stdoutW),
		llmClient,
		"gpt-4o-mini",
		registry,
		"system prompt",
		fake,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"e","payload":{}}` + "\n"))
		time.Sleep(200 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	frames, err := drainFrames(stdoutR)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop: %v", err)
	}

	// The idle frame's running_jobs must echo the fake's count — that's
	// the hint the runner uses to pick its idle-timeout band.
	sawIdleWithJobs := false
	for _, f := range frames {
		if f.Kind == "idle" && f.RunningJobs == 2 {
			sawIdleWithJobs = true
		}
	}
	if !sawIdleWithJobs {
		t.Error("expected an idle frame with running_jobs=2 (the fake's reported count)")
	}
	if got := fake.cleanupCount.Load(); got != 1 {
		t.Errorf("Cleanup should have been called exactly once on shutdown; got %d", got)
	}
}

// TestLoopNotificationDuringEvent pins the headline inbox contract:
// when a background-bash notification arrives WHILE the LLM is
// processing an event, the loop appends it to the conversation so the
// LLM sees it on the next round — even though the in-flight LLM call
// is not interrupted. Without this, completion notifications go on the
// floor whenever the model happens to be thinking.
func TestLoopNotificationDuringEvent(t *testing.T) {
	t.Parallel()

	fake := newFakeBash()

	// Scripted LLM:
	//   call #1: takes ~300ms (mocked via Sleep), returns one tool call
	//            (so a second round happens). During this 300ms we
	//            inject a fake bash notification.
	//   call #2: returns final message. The test checks that the
	//            request body for call #2 includes the notification.
	var (
		call2Body atomic.Value // string
		callCount atomic.Int32
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			// While we slow-walk this call, fire the notification.
			go func() {
				time.Sleep(80 * time.Millisecond)
				fake.notifications <- "[hangrix] task_xyz finished (exit=0)"
			}()
			time.Sleep(250 * time.Millisecond)
			// One tool call so the loop comes back for round 2.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r1",
				"output": []map[string]any{
					{"type": "function_call", "call_id": "tc_1", "name": "glob",
						"arguments": `{"pattern":"*.nonexistent"}`},
				},
				"usage": map[string]any{},
			})
		default:
			body, _ := io.ReadAll(r.Body)
			call2Body.Store(string(body))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r2",
				"output": []map[string]any{
					{"type": "message", "role": "assistant",
						"content": []map[string]any{{"type": "output_text", "text": "done"}}},
				},
				"usage": map[string]any{},
			})
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil)

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	defer stdinR.Close()
	defer stdoutR.Close()

	loop := runtime.NewLoop(
		ipc.NewReader(stdinR),
		ipc.NewWriter(stdoutW),
		llmClient,
		"gpt-4o-mini",
		registry,
		"system prompt",
		fake,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"e","payload":{}}` + "\n"))
		time.Sleep(600 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	if _, err := drainFrames(stdoutR); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop: %v", err)
	}

	body, _ := call2Body.Load().(string)
	if body == "" {
		t.Fatal("LLM call #2 never happened; tool-call round didn't run")
	}
	if !strings.Contains(body, "task_xyz finished") {
		t.Errorf("notification fired mid-call should appear in the next LLM request body; body=%s", body)
	}
}

// TestLoopEventDuringTurnFoldsIn pins the headline mid-turn-event
// contract: when a new `event` frame arrives WHILE the LLM is processing
// an in-flight turn, the agent must fold the event into the current
// conversation so the LLM sees it on the very next round — instead of
// queuing it until the current turn fully terminates. The end-user
// expectation: telling the agent "also do X" while it's mid-tool-loop
// should feel like an immediate interjection, not a request that's
// deferred behind whatever it's doing right now.
//
// We assert two things:
//  1. The second LLM call's request body contains the new event's payload.
//  2. The combined turn emits exactly one `done` frame (the new event is
//     part of the same turn, not a separate one).
func TestLoopEventDuringTurnFoldsIn(t *testing.T) {
	t.Parallel()

	fake := newFakeBash()

	// Scripted LLM:
	//   call #1: stalls ~250ms, returns one tool call so the loop comes
	//            back for round 2. During the stall we push a SECOND
	//            event frame onto stdin.
	//   call #2: returns final message. The test inspects this body for
	//            the second event's marker.
	var (
		call2Body atomic.Value // string
		callCount atomic.Int32
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			time.Sleep(250 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r1",
				"output": []map[string]any{
					{"type": "function_call", "call_id": "tc_1", "name": "glob",
						"arguments": `{"pattern":"*.nonexistent"}`},
				},
				"usage": map[string]any{},
			})
		default:
			body, _ := io.ReadAll(r.Body)
			call2Body.Store(string(body))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r2",
				"output": []map[string]any{
					{"type": "message", "role": "assistant",
						"content": []map[string]any{{"type": "output_text", "text": "done"}}},
				},
				"usage": map[string]any{},
			})
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil)

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	defer stdinR.Close()
	defer stdoutR.Close()

	loop := runtime.NewLoop(
		ipc.NewReader(stdinR),
		ipc.NewWriter(stdoutW),
		llmClient,
		"gpt-4o-mini",
		registry,
		"system prompt",
		fake,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"first","payload":{"marker":"first_event_body"}}` + "\n"))
		// Wait long enough that LLM call #1 is in flight, then push the
		// second event so it lands mid-call.
		time.Sleep(80 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"second","payload":{"marker":"second_event_body"}}` + "\n"))
		time.Sleep(600 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	frames, err := drainFrames(stdoutR)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop: %v", err)
	}

	body, _ := call2Body.Load().(string)
	if body == "" {
		t.Fatal("LLM call #2 never happened; the tool-call round didn't run")
	}
	if !strings.Contains(body, "second_event_body") {
		t.Errorf("mid-turn event should appear in the next LLM request body; body=%s", body)
	}

	// Exactly one done — the absorbed event must NOT spawn a separate
	// turn. If we see two, the loop reverted to the old defer-to-outer
	// behaviour.
	var doneCount, idleCount int
	for _, f := range frames {
		switch f.Kind {
		case "done":
			doneCount++
		case "idle":
			idleCount++
		}
	}
	if doneCount != 1 {
		t.Errorf("mid-turn event should be folded into the current turn (1 done); got %d", doneCount)
	}
	if idleCount != 1 {
		t.Errorf("expected exactly 1 idle frame (one turn); got %d", idleCount)
	}
	if got := callCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls (round 1 had tool calls, round 2 finalised); got %d", got)
	}
}

// TestLoopNotificationDrivesIdleTurn pins the second half of the inbox
// contract: when a notification arrives WHILE the loop is idle (no
// event in progress), it kicks off a brand-new LLM turn so the agent
// gets a chance to react in real time. Without this the agent would
// just sit idle until the next event, defeating the whole point of
// async notifications.
func TestLoopNotificationDrivesIdleTurn(t *testing.T) {
	t.Parallel()

	fake := newFakeBash()

	// Two rounds expected: the first event, then a notification-driven
	// "phantom event". Both end in a final message with no tool calls.
	var callCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "r",
			"output": []map[string]any{
				{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": "ack"}}},
			},
			"usage": map[string]any{},
		})
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil)

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	defer stdinR.Close()
	defer stdoutR.Close()

	loop := runtime.NewLoop(
		ipc.NewReader(stdinR),
		ipc.NewWriter(stdoutW),
		llmClient,
		"gpt-4o-mini",
		registry,
		"system prompt",
		fake,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"first","payload":{}}` + "\n"))
		// Wait for the first event to finish + idle, then push a
		// notification from "outside". The loop should pick it up and
		// drive a second round, even though no event arrived from
		// stdin.
		time.Sleep(300 * time.Millisecond)
		fake.notifications <- "[hangrix] task_late finished"
		time.Sleep(300 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	frames, err := drainFrames(stdoutR)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop: %v", err)
	}

	// Two rounds = two done + two idle frames.
	var done, idle int
	for _, f := range frames {
		switch f.Kind {
		case "done":
			done++
		case "idle":
			idle++
		}
	}
	if done != 2 {
		t.Errorf("expected 2 done frames (event + notification-driven turn); got %d", done)
	}
	if idle != 2 {
		t.Errorf("expected 2 idle frames; got %d", idle)
	}
	if got := callCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls (one per round); got %d", got)
	}
}
