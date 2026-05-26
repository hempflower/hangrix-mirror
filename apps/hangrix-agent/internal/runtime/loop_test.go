package runtime_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/ipc"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/runtime"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/platform"
)

// TestLoopSmoke is the end-to-end rehearsal: scripted LLM (returns
// one tool call, then a final message) + mock platform tools server
// (echoing `issue_read`) + real local tools, driven through the
// runtime loop. We assert the end state from the outbound IPC stream:
//
//   - one local tool call was executed (read on a temp file)
//   - one platform tool call was executed (issue_read)
//   - a final assistant message arrived
//   - a `done` frame closed the turn
//
// This intentionally goes end-to-end through the real ipc/llm/tools
// machinery — only the upstream HTTP servers are mocked. A failure
// here means the seam between two of those packages broke.
func TestLoopSmoke(t *testing.T) {
	t.Parallel()

	// (1) Sandbox file that the local read tool will inspect.
	dir := t.TempDir()
	sandboxFile := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(sandboxFile, []byte("hello from sandbox\n"), 0o644); err != nil {
		t.Fatalf("seed sandbox: %v", err)
	}

	// (2) Mock LLM. First /responses call returns two tool calls (read +
	// stub.ping). Second call (after tool results are fed back) returns
	// a final assistant message with no tool calls — that ends the turn.
	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		// Sanity: first call should not contain "function_call_output"
		// items; second call should — that confirms ToInputItems wires
		// tool results back as the Response API expects.
		isFirst := llmCallCount.Add(1) == 1
		hasFCOutput := strings.Contains(string(body), "function_call_output")
		if isFirst && hasFCOutput {
			t.Errorf("first llm call should not include function_call_output; body=%s", body)
		}
		if !isFirst && !hasFCOutput {
			t.Errorf("second llm call should include function_call_output; body=%s", body)
		}

		w.Header().Set("Content-Type", "application/json")
		if isFirst {
			// Two tool calls: one local (`read`), one platform (`issue_read`).
			resp := map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{
						"type":      "function_call",
						"call_id":   "tc_local_1",
						"name":      "read",
						"arguments": `{"path":"` + sandboxFile + `"}`,
					},
					{
						"type":      "function_call",
						"call_id":   "tc_platform_1",
						"name":      "issue_read",
						"arguments": `{}`,
					},
				},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		// Second turn: plain assistant message, no tool calls → done.
		resp := map[string]any{
			"id": "resp_2",
			"output": []map[string]any{
				{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": "all done"},
					},
				},
			},
			"usage": map[string]any{"input_tokens": 20, "output_tokens": 3, "total_tokens": 23},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(llmServer.Close)

	// (3) Mock platform tools server. The v1 platform client sends
	// REST requests (e.g. GET /issues/current) and expects the v1
	// envelope {"data":...} on success; 4xx errors come back as the
	// structured {"message":"...","errors":[...]} envelope which the
	// platform.Tool layer translates to {"is_error":true,"status":…,
	// "error":"…"} for the registry.  We hard-code one canned reply
	// for issue_read; any other path 404s so a typo surfaces fast.
	platformServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/issues/current" {
			http.Error(w, "unknown tool", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": "method not allowed",
				"errors":  []map[string]any{{"code": "method_not_allowed"}},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"text": "pong"},
		})
	}))
	t.Cleanup(platformServer.Close)

	// (4) Wire the agent the same way main does, but with in/out as
	// in-memory pipes so the test can inject inbound frames and read
	// the outbound stream.
	llmClient := llm.New(llmServer.URL, "test-token")
	platformClient := platform.NewClient(platformServer.URL, "test-token")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, platform.All(platformClient, false), nil, []string{"*"})

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
		bundle.Async,
		0, 0, 0,
	)

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	// Inbound: empty history, then one event, then shutdown.
	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"issue.comment.mentioned","payload":{"body":"@bot inspect the sandbox"}}` + "\n"))
		// Don't close yet — let the loop process the event and emit
		// `done`, then send shutdown.
		time.Sleep(500 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	// Drain outbound and collect frames until the loop exits.
	frames, err := drainFrames(stdoutR)
	if err != nil {
		t.Fatalf("drain frames: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop returned error: %v", err)
	}

	// Assertions: the outbound stream should include
	//   1+ status frames (we don't pin count)
	//   exactly one assistant message with two tool_calls
	//   one tool_call frame for read, one for stub.ping
	//   one assistant message with content "all done", no tool calls
	//   one done frame
	var (
		gotReadCall     bool
		gotPlatformCall bool
		gotDone         bool
		assistantMsgs   int
		finalContent    string
	)
	for _, f := range frames {
		switch f.Kind {
		case "tool_call":
			if f.Name == "read" {
				gotReadCall = true
				if !strings.Contains(string(f.Result), "hello from sandbox") {
					t.Errorf("read tool result missing file content: %s", f.Result)
				}
			}
			if f.Name == "issue_read" {
				gotPlatformCall = true
				if !strings.Contains(string(f.Result), "pong") {
					t.Errorf("issue_read result missing pong: %s", f.Result)
				}
			}
		case "message":
			assistantMsgs++
			if len(f.ToolCalls) == 0 {
				finalContent = f.Content
			}
		case "done":
			gotDone = true
		}
	}
	if !gotReadCall {
		t.Error("expected a tool_call for `read`")
	}
	if !gotPlatformCall {
		t.Error("expected a tool_call for `issue_read`")
	}
	if !gotDone {
		t.Error("expected a `done` frame")
	}
	if assistantMsgs < 2 {
		t.Errorf("expected ≥2 assistant messages (one with tool calls, one final), got %d", assistantMsgs)
	}
	if finalContent != "all done" {
		t.Errorf("final assistant content = %q, want %q", finalContent, "all done")
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls (one with tools, one final), got %d", got)
	}
}

// TestLoopCompactSession verifies the compact_session interception:
// after the LLM calls compact_session(summary=...), the next LLM call's
// request body must NOT contain any pre-compact noise — only the system
// instructions, the summary block, and whatever was appended after.
// This is the regression test for the "messages with role 'tool' must
// be a response to a preceding message with 'tool_calls'" upstream 400
// that the old tail-window trim could trip on.
func TestLoopCompactSession(t *testing.T) {
	t.Parallel()

	var (
		llmCallCount   atomic.Int32
		secondCallBody atomic.Value // []byte
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		callN := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch callN {
		case 1:
			// First turn: take a noisy "tool call" to seed pre-compact
			// history, so the next turn's window can plausibly include
			// orphan tool messages if the compact is wired wrong.
			resp := map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{
						"type":      "function_call",
						"call_id":   "tc_noise",
						"name":      "glob",
						"arguments": `{"pattern":"*.go"}`,
					},
				},
				"usage": map[string]any{"input_tokens": 12, "output_tokens": 4, "total_tokens": 16},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case 2:
			// Second turn: LLM decides to compact. The summary it
			// writes is the only memory the third turn will get.
			resp := map[string]any{
				"id": "resp_2",
				"output": []map[string]any{
					{
						"type":      "function_call",
						"call_id":   "tc_compact",
						"name":      "compact_session",
						"arguments": `{"summary":"investigated repo. nothing actionable. next: handle a fresh event."}`,
					},
				},
				"usage": map[string]any{"input_tokens": 40, "output_tokens": 6, "total_tokens": 46},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case 3:
			// Third turn: this is the call we're really testing. The
			// request body MUST NOT contain "tc_noise" or "function_call"
			// for `glob` — those messages were pre-compact and must have
			// been dropped from the LLM-facing window. Capture the body
			// for assertions below.
			secondCallBody.Store(body)
			resp := map[string]any{
				"id": "resp_3",
				"output": []map[string]any{
					{
						"type": "message",
						"role": "assistant",
						"content": []map[string]any{
							{"type": "output_text", "text": "all clear"},
						},
					},
				},
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 3, "total_tokens": 11},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

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
		bundle.Async,
		0, 0, 0,
	)

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"issue.comment.mentioned","payload":{"body":"@bot look around"}}` + "\n"))
		time.Sleep(500 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	frames, err := drainFrames(stdoutR)
	if err != nil {
		t.Fatalf("drain frames: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 3 {
		t.Fatalf("expected 3 LLM calls (noise + compact + final), got %d", got)
	}

	// The third LLM call body is the regression target.
	rawBody, _ := secondCallBody.Load().([]byte)
	if len(rawBody) == 0 {
		t.Fatal("did not capture third LLM call body")
	}
	body := string(rawBody)
	// tc_noise is unique to the pre-compact tool call_id, so its
	// presence in the post-compact body is the unambiguous signal that
	// older history leaked through the summary anchor.
	if strings.Contains(body, "tc_noise") {
		t.Errorf("post-compact LLM body still contains pre-compact tool call id `tc_noise`:\n%s", body)
	}
	// The compact_session call itself is pre-compact noise once the
	// summary is in place — its call_id must also be dropped from
	// `input`. (It still appears in `tools` as a registered function;
	// that's expected.)
	if strings.Contains(body, "tc_compact") {
		t.Errorf("post-compact LLM body still contains compact_session call id `tc_compact`:\n%s", body)
	}
	if !strings.Contains(body, "previous_session_summary") {
		t.Errorf("post-compact LLM body missing summary wrapper:\n%s", body)
	}
	if !strings.Contains(body, "handle a fresh event") {
		t.Errorf("post-compact LLM body missing the summary text the LLM wrote:\n%s", body)
	}
	// Structural check: input array should be the summary alone. We
	// parse the body and walk `input` so the assertion is robust to
	// JSON field ordering.
	var parsed struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		t.Fatalf("decode post-compact body: %v", err)
	}
	if len(parsed.Input) != 1 {
		t.Errorf("post-compact input has %d items, want 1 (just the summary); items=%+v", len(parsed.Input), parsed.Input)
	}
	for _, item := range parsed.Input {
		itemType, _ := item["type"].(string)
		if itemType == "function_call" || itemType == "function_call_output" {
			// Any function_call or function_call_output in the post-
			// compact window would be an orphan w.r.t. the now-truncated
			// history — exactly the shape that triggered the original
			// upstream 400.
			callID, _ := item["call_id"].(string)
			t.Errorf("post-compact input leaked %s (call_id=%s); window should contain only the summary", itemType, callID)
		}
	}

	// The compact_session tool_call frame must still have been emitted
	// outbound so the runner/platform can persist it — losing the
	// summary on the audit trail would defeat round-trip on session
	// re-attach.
	var sawCompactFrame bool
	for _, f := range frames {
		if f.Kind == "tool_call" && f.Name == "compact_session" {
			sawCompactFrame = true
			if !strings.Contains(string(f.Args), "handle a fresh event") {
				t.Errorf("compact_session tool_call args missing the summary text: %s", f.Args)
			}
		}
	}
	if !sawCompactFrame {
		t.Error("expected an outbound tool_call frame for compact_session")
	}
}

// TestLoopAtMentionNudge verifies the at-mention reminder: when the
// model returns plain assistant text containing `@` and no tool calls,
// the loop should NOT close the turn — it must inject a system_reminder
// telling the model to use the issue_comment tool, then make a second
// LLM call so the model can retry. We assert two LLM calls happened,
// the second saw the reminder, and the turn closed cleanly afterwards.
func TestLoopAtMentionNudge(t *testing.T) {
	t.Parallel()

	var (
		llmCallCount   atomic.Int32
		secondCallBody atomic.Value // []byte
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		callN := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch callN {
		case 1:
			// First turn: the model "forgets" to use issue_comment and
			// just emits a plain assistant message with an @-mention.
			resp := map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{
						"type": "message",
						"role": "assistant",
						"content": []map[string]any{
							{"type": "output_text", "text": "@agent-frontend please review"},
						},
					},
				},
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 4, "total_tokens": 12},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case 2:
			// Second turn: the loop should have injected the reminder.
			// Capture the body so we can assert it.
			secondCallBody.Store(body)
			resp := map[string]any{
				"id": "resp_2",
				"output": []map[string]any{
					{
						"type": "message",
						"role": "assistant",
						"content": []map[string]any{
							{"type": "output_text", "text": "acknowledged, will retry via tool"},
						},
					},
				},
				"usage": map[string]any{"input_tokens": 20, "output_tokens": 5, "total_tokens": 25},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

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
		bundle.Async,
		0, 0, 0,
	)

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"issue.comment.mentioned","payload":{"body":"hello"}}` + "\n"))
		time.Sleep(500 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	frames, err := drainFrames(stdoutR)
	if err != nil {
		t.Fatalf("drain frames: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Fatalf("expected 2 LLM calls (nudge forces a retry), got %d", got)
	}
	rawBody, _ := secondCallBody.Load().([]byte)
	if len(rawBody) == 0 {
		t.Fatal("did not capture second LLM call body")
	}
	body := string(rawBody)
	if !strings.Contains(body, "issue_comment") {
		t.Errorf("second LLM call body missing the at-mention reminder pointing at issue_comment:\n%s", body)
	}
	if !strings.Contains(body, "system_reminder") {
		t.Errorf("second LLM call body missing the system_reminder wrapper:\n%s", body)
	}

	// Outbound stream should show both assistant messages and exactly
	// one `done` frame (the second turn closed the loop).
	var (
		assistantMsgs int
		doneFrames    int
	)
	for _, f := range frames {
		switch f.Kind {
		case "message":
			assistantMsgs++
		case "done":
			doneFrames++
		}
	}
	if assistantMsgs != 2 {
		t.Errorf("expected 2 assistant messages, got %d", assistantMsgs)
	}
	if doneFrames != 1 {
		t.Errorf("expected exactly 1 done frame, got %d", doneFrames)
	}
}

// TestLoopAtMentionNudgeWithToolCallsPreservesChain is the regression
// guard for the bug where the @-mention reminder was injected between
// an assistant(tool_calls=…) entry and its tool_result(s). Upstream
// requires the tool_result items to immediately follow the assistant
// message that produced the tool_calls — any user-role item wedged in
// between makes the API reject the next call with a 400.
//
// Scenario: the first LLM call returns BOTH `@`-text content AND a
// tool_call. The loop must (a) append the assistant message, (b)
// dispatch the tool and append its result, and only THEN (c) append
// the @-mention nudge. We verify the order of input items on the
// second LLM call body.
func TestLoopAtMentionNudgeWithToolCallsPreservesChain(t *testing.T) {
	t.Parallel()

	var (
		llmCallCount   atomic.Int32
		secondCallBody atomic.Value // []byte
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		callN := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch callN {
		case 1:
			// Assistant returns `@` text AND a tool call in the same
			// response. The tool name doesn't have to exist — the
			// registry will surface an error result, which is still
			// a valid function_call_output that needs to be paired
			// with the function_call before any user-role item.
			resp := map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{
						"type": "message",
						"role": "assistant",
						"content": []map[string]any{
							{"type": "output_text", "text": "@agent-frontend please review"},
						},
					},
					{
						"type":      "function_call",
						"call_id":   "call_abc",
						"name":      "noop_unknown_tool",
						"arguments": "{}",
					},
				},
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 4, "total_tokens": 12},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case 2:
			secondCallBody.Store(body)
			resp := map[string]any{
				"id": "resp_2",
				"output": []map[string]any{
					{
						"type": "message",
						"role": "assistant",
						"content": []map[string]any{
							{"type": "output_text", "text": "ack"},
						},
					},
				},
				"usage": map[string]any{"input_tokens": 20, "output_tokens": 1, "total_tokens": 21},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

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
		bundle.Async,
		0, 0, 0,
	)

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"issue.comment.mentioned","payload":{"body":"hello"}}` + "\n"))
		time.Sleep(500 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	if _, err := drainFrames(stdoutR); err != nil {
		t.Fatalf("drain frames: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", got)
	}

	rawBody, _ := secondCallBody.Load().([]byte)
	if len(rawBody) == 0 {
		t.Fatal("did not capture second LLM call body")
	}

	var parsed struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		t.Fatalf("unmarshal second call body: %v", err)
	}

	// Find the function_call we returned in turn 1 and assert the very
	// next item is its function_call_output (NOT a user-role reminder).
	var fcIdx = -1
	for i, item := range parsed.Input {
		if item["type"] == "function_call" && item["call_id"] == "call_abc" {
			fcIdx = i
			break
		}
	}
	if fcIdx < 0 {
		t.Fatalf("function_call for call_abc not found in second call input:\n%s", string(rawBody))
	}
	if fcIdx+1 >= len(parsed.Input) {
		t.Fatalf("function_call is the last input item — no tool result followed:\n%s", string(rawBody))
	}
	next := parsed.Input[fcIdx+1]
	if next["type"] != "function_call_output" || next["call_id"] != "call_abc" {
		t.Fatalf("expected function_call_output immediately after function_call, got %v:\n%s", next, string(rawBody))
	}

	// And the system_reminder must appear, but only AFTER the tool result.
	var reminderIdx = -1
	for i, item := range parsed.Input {
		if item["role"] != "user" {
			continue
		}
		content, _ := item["content"].([]any)
		if len(content) == 0 {
			continue
		}
		first, _ := content[0].(map[string]any)
		text, _ := first["text"].(string)
		if strings.Contains(text, "system_reminder") && strings.Contains(text, "issue_comment") {
			reminderIdx = i
			break
		}
	}
	if reminderIdx < 0 {
		t.Fatalf("at-mention reminder not found in second call input:\n%s", string(rawBody))
	}
	if reminderIdx <= fcIdx+1 {
		t.Fatalf("at-mention reminder (idx=%d) must come AFTER the tool result (idx=%d), but didn't — chain is broken:\n%s", reminderIdx, fcIdx+1, string(rawBody))
	}
}

// TestLoopSleepGate verifies the sleep batching guard: when the LLM
// returns sleep alongside other tool calls in the same response, the
// loop must only execute sleep and reject the rest with errors. The
// turn must end immediately (done frame) so the agent goes idle and
// waits for the sleep wake-up notification.
func TestLoopSleepGate(t *testing.T) {
	t.Parallel()

	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		callN := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch callN {
		case 1:
			// First turn: LLM returns both sleep AND another tool call
			// (a naive read). The sleep-gate must reject the read.
			resp := map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{
						"type":      "function_call",
						"call_id":   "tc_sleep",
						"name":      "sleep",
						"arguments": `{"seconds":5}`,
					},
					{
						"type":      "function_call",
						"call_id":   "tc_other",
						"name":      "glob",
						"arguments": `{"pattern":"*.go"}`,
					},
				},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

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
		bundle.Async,
		0, 0, 0,
	)

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"issue.comment.mentioned","payload":{"body":"@bot do something"}}` + "\n"))
		time.Sleep(500 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	frames, err := drainFrames(stdoutR)
	if err != nil {
		t.Fatalf("drain frames: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 1 {
		t.Fatalf("expected exactly 1 LLM call (sleep-gate prevents second round), got %d", got)
	}

	var (
		sawSleepCall bool
		sawOtherCall bool
		sawDone      bool
		otherResult  string
	)
	for _, f := range frames {
		switch f.Kind {
		case "tool_call":
			if f.Name == "sleep" {
				sawSleepCall = true
				if !strings.Contains(string(f.Result), "scheduled") {
					t.Errorf("sleep result missing 'scheduled': %s", f.Result)
				}
			}
			if f.Name == "glob" {
				sawOtherCall = true
				otherResult = string(f.Result)
			}
		case "done":
			sawDone = true
		}
	}
	if !sawSleepCall {
		t.Error("expected a tool_call for sleep")
	}
	if !sawOtherCall {
		t.Error("expected a tool_call for the rejected call (glob)")
	}
	if !strings.Contains(otherResult, "batched with sleep") {
		t.Errorf("rejected call result should explain batching violation, got: %s", otherResult)
	}
	if !sawDone {
		t.Error("expected a done frame after sleep-gate")
	}
}

// TestLoopReasoningTimeoutRetrySuccess verifies the retry path: when the
// first LLM attempt hits a reasoning timeout (DeadlineExceeded), the loop
// retries with the same request snapshot. The second attempt succeeds.
func TestLoopReasoningTimeoutRetrySuccess(t *testing.T) {
	t.Parallel()

	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		n := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			// Sleep long enough that the per-call timeout fires, causing
			// the HTTP client to return DeadlineExceeded.
			time.Sleep(200 * time.Millisecond)
			// Response after timeout — the client has already bailed,
			// so this goes nowhere, but write it anyway.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_timeout",
				"output": []map[string]any{
					{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "too late"}}},
				},
				"usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
			})
		case 2:
			// Retry attempt: respond immediately with a plain message.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_ok",
				"output": []map[string]any{
					{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "retry succeeded"}}},
				},
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 4, "total_tokens": 12},
			})
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

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
		bundle.Async,
		0,
		100*time.Millisecond, // reasoningTimeout
		1,                    // reasoningTimeoutRetries: 1 retry = 2 total attempts
	)

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"issue.comment.mentioned","payload":{"body":"@bot hello"}}` + "\n"))
		time.Sleep(time.Second)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	if _, err := drainFrames(stdoutR); err != nil {
		t.Fatalf("drain frames: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls (1 timeout + 1 retry), got %d", got)
	}
}

// TestLoopReasoningTimeoutRetryExhausted verifies that after maxAttempts
// calls, each returning DeadlineExceeded, the loop stops and returns a
// descriptive timeout error.
func TestLoopReasoningTimeoutRetryExhausted(t *testing.T) {
	t.Parallel()

	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		llmCallCount.Add(1)
		// Always sleep past the timeout so every call returns DeadlineExceeded.
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_late",
			"output": []map[string]any{
				{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "too late"}}},
			},
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
		})
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

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
		bundle.Async,
		0,
		100*time.Millisecond, // reasoningTimeout
		1,                    // reasoningTimeoutRetries: 2 total attempts
	)

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"issue.comment.mentioned","payload":{"body":"@bot hello"}}` + "\n"))
		time.Sleep(time.Second)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	if _, err := drainFrames(stdoutR); err != nil {
		t.Fatalf("drain frames: %v", err)
	}
	err := <-loopErr
	if err == nil {
		t.Fatal("expected loop to return a timeout error, got nil")
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls (both timing out), got %d", got)
	}
	if !strings.Contains(err.Error(), "LLM reasoning timeout after 2 attempt(s)") {
		t.Errorf("error message should indicate exhausted retries, got: %v", err)
	}
	if !strings.Contains(err.Error(), "threshold=0s") {
		t.Errorf("error message should include threshold, got: %v", err)
	}
}

// TestLoopReasoningTimeoutNonTimeoutErrorNoRetry verifies that non-timeout
// errors (here a 400 from the upstream) are NOT retried at the reasoning-
// timeout layer. Only one LLM call should be made.
func TestLoopReasoningTimeoutNonTimeoutErrorNoRetry(t *testing.T) {
	t.Parallel()

	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		llmCallCount.Add(1)
		// Return a hard 400 — not a timeout, not retryable at either layer.
		http.Error(w, "upstream rejected request", http.StatusBadRequest)
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

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
		bundle.Async,
		0,
		1*time.Second, // reasoningTimeout
		2,             // reasoningTimeoutRetries: 3 would be possible, but should only use 1
	)

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"issue.comment.mentioned","payload":{"body":"@bot hello"}}` + "\n"))
		time.Sleep(500 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	if _, err := drainFrames(stdoutR); err != nil {
		t.Fatalf("drain frames: %v", err)
	}
	err := <-loopErr
	if err == nil {
		t.Fatal("expected loop to return an error for the 400, got nil")
	}
	if got := llmCallCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 LLM call (no retry for non-timeout error), got %d", got)
	}
	if !strings.Contains(err.Error(), "llm call failed") {
		t.Errorf("error should be propagated from the LLM layer, got: %v", err)
	}
}

// TestLoopReasoningTimeoutInboxDrain verifies that inbox events arriving
// while an LLM call is in flight are still drained into context, causing
// the loop to make another round with the new input visible.
func TestLoopReasoningTimeoutInboxDrain(t *testing.T) {
	t.Parallel()

	// Block the first LLM call on a channel so the test has a window to
	// push an inbox event mid-call.
	proceedCh := make(chan struct{})
	var (
		llmCallCount   atomic.Int32
		secondCallBody atomic.Value // []byte
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		n := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			// Block until the test pushes the inbox event.
			<-proceedCh
			// Respond with a plain assistant message — no tool calls.
			// The loop will detect postCallLen > preCallLen (the inbox
			// event folded into context) and make a second round.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "thinking..."}}},
				},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13},
			})
		case 2:
			// Capture the second call body so we can assert the inbox
			// event text is visible.
			body, _ := io.ReadAll(r.Body)
			secondCallBody.Store(body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_2",
				"output": []map[string]any{
					{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "all done"}}},
				},
				"usage": map[string]any{"input_tokens": 5, "output_tokens": 2, "total_tokens": 7},
			})
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

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
		bundle.Async,
		0, 5*time.Second, 0, // reasoning timeout generous; retries=0 (1 total attempt)
	)

	loopErr := make(chan error, 1)
	go func() {
		loopErr <- loop.Run(ctx)
		stdoutW.Close()
	}()

	go func() {
		_, _ = stdinW.Write([]byte(`{"kind":"history","messages":[]}` + "\n"))
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"issue.comment.mentioned","payload":{"body":"@bot hello"}}` + "\n"))
		// Wait for the first LLM call to start (it blocks on proceedCh).
		time.Sleep(100 * time.Millisecond)
		// Push an inbox event while the LLM call is in flight.
		_, _ = stdinW.Write([]byte(`{"kind":"event","event":"issue.comment.mentioned","payload":{"body":"INBOX_EVENT_MARKER"}}` + "\n"))
		// Give the main goroutine time to drain the inbox via select.
		time.Sleep(100 * time.Millisecond)
		// Unblock the first LLM call.
		close(proceedCh)
		// Wait for the second round to finish, then shutdown.
		time.Sleep(500 * time.Millisecond)
		_, _ = stdinW.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n"))
		stdinW.Close()
	}()

	frames, err := drainFrames(stdoutR)
	if err != nil {
		t.Fatalf("drain frames: %v", err)
	}
	if err := <-loopErr; err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Fatalf("expected 2 LLM calls (first unblocked + second round for inbox event), got %d", got)
	}

	// The inbox event text must be visible in the second LLM call's input.
	rawBody, _ := secondCallBody.Load().([]byte)
	if len(rawBody) == 0 {
		t.Fatal("did not capture second LLM call body")
	}
	if !strings.Contains(string(rawBody), "INBOX_EVENT_MARKER") {
		t.Errorf("second LLM call body should contain the inbox event text, got:\n%s", string(rawBody))
	}

	// Outbound stream should have exactly one done frame.
	var doneFrames int
	for _, f := range frames {
		if f.Kind == "done" {
			doneFrames++
		}
	}
	if doneFrames != 1 {
		t.Errorf("expected exactly 1 done frame, got %d", doneFrames)
	}
}

// drainFrames reads outbound JSON-Lines until EOF.
func drainFrames(r io.Reader) ([]ipc.Outbound, error) {
	dec := json.NewDecoder(r)
	var out []ipc.Outbound
	for {
		var f ipc.Outbound
		if err := dec.Decode(&f); err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, err
		}
		out = append(out, f)
	}
}
