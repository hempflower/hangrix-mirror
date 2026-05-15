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
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/mcp"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/runtime"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
)

// TestLoopSmoke is the M6b exit-condition rehearsal: scripted LLM
// (returns one tool call, then a final message) + mock MCP (one stub
// platform tool) + real local tools, driven through the runtime loop.
// We assert the end state from the outbound IPC stream:
//
//   - one local tool call was executed (read on a temp file)
//   - one MCP tool call was executed (the stub)
//   - a final assistant message arrived
//   - a `done` frame closed the turn
//
// This intentionally goes end-to-end through the real ipc/llm/mcp
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
			// Two tool calls: one local (`read`), one MCP (`stub.ping`).
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
						"call_id":   "tc_mcp_1",
						"name":      "stub.ping",
						"arguments": `{"who":"agent"}`,
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

	// (3) Mock MCP server. tools/list returns one stub tool;
	// tools/call returns a fixed text content payload.
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var rpc struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &rpc); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		var result any
		switch rpc.Method {
		case "tools/list":
			result = map[string]any{
				"tools": []map[string]any{{
					"name":        "stub.ping",
					"description": "reply with pong",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"who": map[string]any{"type": "string"},
						},
					},
				}},
			}
		case "tools/call":
			result = map[string]any{
				"isError": false,
				"content": []map[string]any{
					{"type": "text", "text": "pong"},
				},
			}
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
			return
		}
		out := map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(mcpServer.Close)

	// (4) Wire the agent the same way main does, but with in/out as
	// in-memory pipes so the test can inject inbound frames and read
	// the outbound stream.
	llmClient := llm.New(llmServer.URL, "test-token")
	mcpClient := mcp.New(mcpServer.URL, "test-token")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	registry, err := tools.Build(ctx, local.All(), mcpClient, nil)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}

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
		gotReadCall    bool
		gotStubCall    bool
		gotDone        bool
		assistantMsgs  int
		finalContent   string
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
			if f.Name == "stub.ping" {
				gotStubCall = true
				if !strings.Contains(string(f.Result), "pong") {
					t.Errorf("stub.ping result missing pong: %s", f.Result)
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
	if !gotStubCall {
		t.Error("expected a tool_call for `stub.ping`")
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
