package local_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
)

// llmServerFunc is the per-call shape used by startMockLLM: given the
// 1-based call index and the raw POST body, return the wire-level response
// the mock should serve. The function is called under a mutex so racing
// children's calls can't reorder the assertion the test wants to make.
type llmServerFunc func(callIdx int, body []byte) (status int, respBody map[string]any)

// startMockLLM stands up a fake /api/llm/v1 endpoint. The returned counter
// lets the test verify "the validation rejected the call before reaching
// the LLM" cases without inspecting bodies. Cleanup is registered against
// t so the goroutine never leaks past the test.
func startMockLLM(t *testing.T, fn llmServerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		idx := int(calls.Add(1))
		body, _ := io.ReadAll(r.Body)
		status, resp := fn(idx, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// finalMessage builds a Response-API body whose only output item is the
// assistant's final text — no tool_calls. A child receiving this terminates
// the loop and reports outcome="ok".
func finalMessage(text string) map[string]any {
	return map[string]any{
		"id": "resp_final",
		"output": []map[string]any{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": text},
				},
			},
		},
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
	}
}

// toolCallWithText builds a body that drives the child loop forward: one
// assistant message carrying both text (so we can verify last-assistant-
// text propagation) and one function_call that the child will dispatch.
func toolCallWithText(callID, name, args, text string) map[string]any {
	return map[string]any{
		"id": "resp_tc",
		"output": []map[string]any{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": text},
				},
			},
			{
				"type":      "function_call",
				"call_id":   callID,
				"name":      name,
				"arguments": args,
			},
		},
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
	}
}

// TestResearchReturnsSummariesInOrder verifies the most basic guarantee: N
// independent tasks each get their own sub-agent loop, and the returned
// results array is ordered to match the tasks array regardless of which
// goroutine finished first.
//
// The mock LLM dispatches by inspecting the request body (each task's
// prompt carries a unique tag), so the assertion does not depend on call
// arrival order — concurrent goroutines may race the server in either
// direction and the test must still pass.
func TestResearchReturnsSummariesInOrder(t *testing.T) {
	t.Parallel()
	srv, _ := startMockLLM(t, func(idx int, body []byte) (int, map[string]any) {
		switch {
		case bytes.Contains(body, []byte("task-A")):
			return http.StatusOK, finalMessage("summary for A")
		case bytes.Contains(body, []byte("task-B")):
			return http.StatusOK, finalMessage("summary for B")
		case bytes.Contains(body, []byte("task-C")):
			return http.StatusOK, finalMessage("summary for C")
		}
		return http.StatusInternalServerError, map[string]any{"error": "unrecognised prompt"}
	})

	client := llm.New(srv.URL, "test-token")
	tool := local.NewResearchTool(client, "test-model")

	raw := mustJSON(map[string]any{
		"tasks": []map[string]any{
			{"prompt": "task-A: investigate the A region"},
			{"prompt": "task-B: investigate the B region"},
			{"prompt": "task-C: investigate the C region"},
		},
	})
	res, err := tool.Call(context.Background(), raw)
	if err != nil {
		t.Fatalf("research: %v", err)
	}
	got := decodeResults(t, res)
	if len(got) != 3 {
		t.Fatalf("results=%d, want 3", len(got))
	}
	want := []string{"summary for A", "summary for B", "summary for C"}
	for i, r := range got {
		if r.Outcome != "ok" {
			t.Errorf("results[%d].outcome=%q want ok", i, r.Outcome)
		}
		if r.Summary != want[i] {
			t.Errorf("results[%d].summary=%q want %q", i, r.Summary, want[i])
		}
		if r.StepsUsed != 1 {
			t.Errorf("results[%d].steps_used=%d want 1", i, r.StepsUsed)
		}
	}
}

// TestResearchTaskCapEnforced pins the 10-task ceiling. The check MUST
// reject before any LLM call — otherwise the cost-guard semantics break
// (an attacker / runaway LLM could fan out to N=1000 if validation only
// happens after dispatch).
func TestResearchTaskCapEnforced(t *testing.T) {
	t.Parallel()
	srv, calls := startMockLLM(t, func(idx int, body []byte) (int, map[string]any) {
		t.Errorf("LLM was called despite task-count validation; should reject up-front")
		return http.StatusOK, finalMessage("should not reach")
	})
	client := llm.New(srv.URL, "test-token")
	tool := local.NewResearchTool(client, "test-model")

	tasks := make([]map[string]any, 11)
	for i := range tasks {
		tasks[i] = map[string]any{"prompt": fmt.Sprintf("p%d", i)}
	}
	_, err := tool.Call(context.Background(), mustJSON(map[string]any{"tasks": tasks}))
	if err == nil {
		t.Fatal("expected error for >10 tasks")
	}
	if !strings.Contains(err.Error(), "exceeds the per-call limit") {
		t.Errorf("error should explain the limit; got: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("LLM called %d times; want 0 (validation must reject before any call)", got)
	}
}

// TestResearchMaxStepsCapEnforced pins the 9999 hard cap on per-task
// max_steps. Like the task-count cap, this MUST trip before any LLM call
// — otherwise the cap is just a soft suggestion.
func TestResearchMaxStepsCapEnforced(t *testing.T) {
	t.Parallel()
	srv, calls := startMockLLM(t, func(idx int, body []byte) (int, map[string]any) {
		t.Errorf("LLM was called despite max_steps validation")
		return http.StatusOK, finalMessage("should not reach")
	})
	client := llm.New(srv.URL, "test-token")
	tool := local.NewResearchTool(client, "test-model")

	_, err := tool.Call(context.Background(), mustJSON(map[string]any{
		"tasks": []map[string]any{
			{"prompt": "p", "max_steps": 10000},
		},
	}))
	if err == nil {
		t.Fatal("expected error for max_steps > 9999")
	}
	if !strings.Contains(err.Error(), "exceeds the hard cap") {
		t.Errorf("error should explain the cap; got: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("LLM called %d times; want 0", got)
	}
}

// TestResearchEmptyPromptRejected stops the obvious footgun of an empty
// brief slipping through and burning a full LLM round-trip on a no-op
// prompt. Validation happens up-front so the LLM is never reached.
func TestResearchEmptyPromptRejected(t *testing.T) {
	t.Parallel()
	srv, calls := startMockLLM(t, func(idx int, body []byte) (int, map[string]any) {
		return http.StatusOK, finalMessage("never")
	})
	client := llm.New(srv.URL, "test-token")
	tool := local.NewResearchTool(client, "test-model")

	_, err := tool.Call(context.Background(), mustJSON(map[string]any{
		"tasks": []map[string]any{{"prompt": "   "}},
	}))
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("LLM called %d times; want 0", got)
	}
}

// TestResearchStepLimitReached pins the step-budget exit path. The mock
// keeps returning a `read` tool call so the child never naturally
// terminates; after `max_steps` rounds we expect outcome=step_limit,
// steps_used equal to the cap, and Summary equal to the LAST assistant
// text we observed — the "use last assistant text" contract the parent
// agent relies on.
func TestResearchStepLimitReached(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	seedFile := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seedFile, []byte("anything"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _ := startMockLLM(t, func(callIdx int, body []byte) (int, map[string]any) {
		text := fmt.Sprintf("round %d", callIdx)
		args := fmt.Sprintf(`{"path":%q}`, seedFile)
		return http.StatusOK, toolCallWithText(fmt.Sprintf("tc_%d", callIdx), "read", args, text)
	})
	client := llm.New(srv.URL, "test-token")
	tool := local.NewResearchTool(client, "test-model")

	res, err := tool.Call(context.Background(), mustJSON(map[string]any{
		"tasks": []map[string]any{
			{"prompt": "loop forever", "max_steps": 3},
		},
	}))
	if err != nil {
		t.Fatalf("research: %v", err)
	}
	got := decodeResults(t, res)
	if len(got) != 1 {
		t.Fatalf("results=%d, want 1", len(got))
	}
	r := got[0]
	if r.Outcome != "step_limit" {
		t.Errorf("outcome=%q want step_limit", r.Outcome)
	}
	if r.StepsUsed != 3 {
		t.Errorf("steps_used=%d want 3", r.StepsUsed)
	}
	if r.Summary != "round 3" {
		t.Errorf("summary=%q want %q (last assistant text)", r.Summary, "round 3")
	}
}

// TestResearchSubAgentCatalogueIsReadOnly confirms the catalogue the child
// hands to its LLM omits every write-y tool. We inspect the raw POST body
// because the seam where the LLM "sees" the catalogue is the wire — that
// is the surface the child cannot cheat on.
//
// If a future change accidentally exposes `bash` to children, this test
// trips and tells you so before a runaway sub-agent ships an `rm -rf`.
func TestResearchSubAgentCatalogueIsReadOnly(t *testing.T) {
	t.Parallel()
	var (
		firstBody []byte
		captured  atomic.Bool
	)
	srv, _ := startMockLLM(t, func(callIdx int, body []byte) (int, map[string]any) {
		if captured.CompareAndSwap(false, true) {
			firstBody = append([]byte(nil), body...)
		}
		return http.StatusOK, finalMessage("done")
	})
	client := llm.New(srv.URL, "test-token")
	tool := local.NewResearchTool(client, "test-model")
	if _, err := tool.Call(context.Background(), mustJSON(map[string]any{
		"tasks": []map[string]any{{"prompt": "p"}},
	})); err != nil {
		t.Fatalf("research: %v", err)
	}

	for _, name := range []string{"write", "edit", "bash", "research", "issue_comment", "issue_merge"} {
		if bytes.Contains(firstBody, []byte(`"name":"`+name+`"`)) {
			t.Errorf("sub-agent catalogue must not advertise %q; first request body: %s", name, firstBody)
		}
	}
	for _, name := range []string{"read", "glob", "grep", "webfetch"} {
		if !bytes.Contains(firstBody, []byte(`"name":"`+name+`"`)) {
			t.Errorf("sub-agent catalogue should advertise %q; first request body: %s", name, firstBody)
		}
	}
}

// TestResearchRunsTasksConcurrently is the timing-based proof that the
// fan-out is actually parallel. Sequential dispatch would take N × delay;
// we assert wall-clock < 2 × delay, which gives plenty of margin for
// scheduling jitter on a busy CI box while still failing loudly if a
// future refactor accidentally drops the goroutine.
//
// The delay is generous (200 ms) so a slow GC pause doesn't flake the
// test; the cushion below it (2×) is generous so a transient CI hiccup
// doesn't flake either.
func TestResearchRunsTasksConcurrently(t *testing.T) {
	t.Parallel()
	const perCallDelay = 200 * time.Millisecond
	const numTasks = 4
	srv, _ := startMockLLM(t, func(callIdx int, body []byte) (int, map[string]any) {
		time.Sleep(perCallDelay)
		return http.StatusOK, finalMessage("done")
	})
	client := llm.New(srv.URL, "test-token")
	tool := local.NewResearchTool(client, "test-model")

	tasks := make([]map[string]any, numTasks)
	for i := range tasks {
		tasks[i] = map[string]any{"prompt": fmt.Sprintf("task-%d", i)}
	}
	start := time.Now()
	if _, err := tool.Call(context.Background(), mustJSON(map[string]any{"tasks": tasks})); err != nil {
		t.Fatalf("research: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed >= 2*perCallDelay {
		t.Errorf("expected parallel dispatch (≈%v), got %v — looks serial (%dx delay)",
			perCallDelay, elapsed, int(elapsed/perCallDelay))
	}
}

// TestResearchContextCancelAbortsChildren proves cancellation propagates
// from the parent's tool_call ctx down through every in-flight sub-agent.
// Without this, a parent-side timeout would orphan goroutines on the
// long-running LLM client call.
//
// The mock handler watches r.Context() (which httptest cancels when the
// client disconnects) so srv.Close in t.Cleanup doesn't have to wait for
// the sleep to finish — without that, the test runtime would be dominated
// by cleanup, not by the path it's actually exercising.
func TestResearchContextCancelAbortsChildren(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(finalMessage("late"))
		case <-r.Context().Done():
			return
		}
	}))
	t.Cleanup(srv.Close)

	client := llm.New(srv.URL, "test-token")
	tool := local.NewResearchTool(client, "test-model")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	res, err := tool.Call(ctx, mustJSON(map[string]any{
		"tasks": []map[string]any{{"prompt": "stuck"}},
	}))
	elapsed := time.Since(start)
	if elapsed >= 2*time.Second {
		t.Fatalf("ctx cancel did not abort the child: elapsed=%v", elapsed)
	}
	if err != nil {
		// Acceptable: the tool surfaces ctx.Err() directly. Nothing else
		// to verify in that branch.
		return
	}
	got := decodeResults(t, res)
	if len(got) != 1 || got[0].Outcome != "error" {
		t.Errorf("expected one error result on cancel; got %+v", got)
	}
}

// researchResultView matches the JSON shape researchResult marshals into.
// Kept local so the test file does not import any unexported type from
// the local package.
type researchResultView struct {
	Outcome   string `json:"outcome"`
	Summary   string `json:"summary"`
	StepsUsed int    `json:"steps_used"`
	Error     string `json:"error"`
}

func decodeResults(t *testing.T, v any) []researchResultView {
	t.Helper()
	out := mustReJSON(v)
	var wrap struct {
		Results []researchResultView `json:"results"`
	}
	if err := json.Unmarshal(out, &wrap); err != nil {
		t.Fatalf("decode research result: %v (%s)", err, out)
	}
	return wrap.Results
}
