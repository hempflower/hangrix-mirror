package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
)

func TestGuardResult_PassesSmallPayload(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"key":"value"}`)
	got := guardResult(input)
	if string(got) != string(input) {
		t.Errorf("small payload should pass through unchanged; got %s", got)
	}
}

func TestGuardResult_TruncatesLargePayload(t *testing.T) {
	t.Parallel()
	// Build a payload that is larger than the budget.
	big := strings.Repeat("x", defaultResultBudgetBytes+1024)
	input, err := json.Marshal(map[string]any{"markdown": big})
	if err != nil {
		t.Fatal(err)
	}
	if len(input) <= defaultResultBudgetBytes {
		t.Fatalf("test input not large enough: %d bytes (need > %d)", len(input), defaultResultBudgetBytes)
	}
	got := guardResult(input)

	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("guard output not valid JSON: %v; raw=%s", err, got)
	}

	// Must have truncation metadata.
	if truncated, ok := m["truncated"].(bool); !ok || !truncated {
		t.Errorf("expected truncated=true, got %v", m["truncated"])
	}
	notice, ok := m["truncation_notice"].(string)
	if !ok || notice == "" {
		t.Errorf("expected non-empty truncation_notice, got %q", notice)
	}
	outFile, ok := m["output_file"].(string)
	if !ok || outFile == "" {
		t.Errorf("expected non-empty output_file, got %q", outFile)
	}

	// output_file must point to an existing file with the full content.
	full, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("output_file %q not readable: %v", outFile, err)
	}
	if !strings.Contains(string(full), big[:256]) {
		t.Errorf("output_file should contain the full primary content; got %d bytes", len(full))
	}

	// markdown field must be truncated.
	md, _ := m["markdown"].(string)
	if md == "" {
		t.Errorf("markdown field should be present (truncated)")
	}
	if len(md) >= len(big) {
		t.Errorf("markdown should be truncated: len=%d vs original=%d", len(md), len(big))
	}

	// Result must fit within budget.
	if len(got) > defaultResultBudgetBytes {
		t.Errorf("truncated result still exceeds budget: %d > %d", len(got), defaultResultBudgetBytes)
	}
}

func TestGuardResult_ReusesExistingOutputFile(t *testing.T) {
	t.Parallel()
	// Simulate a bash result that already has output_file set.
	existingPath := "/tmp/hangrix-bash-test.log"
	big := strings.Repeat("y", defaultResultBudgetBytes+2048)
	input, err := json.Marshal(map[string]any{
		"output":      big,
		"output_file": existingPath,
		"exit_code":   0,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := guardResult(input)
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	// output_file should still be the original path.
	if m["output_file"] != existingPath {
		t.Errorf("output_file changed: got %q, want %q", m["output_file"], existingPath)
	}
}

func TestGuardResult_ScalarUnchanged(t *testing.T) {
	t.Parallel()
	// Scalars (not objects) pass through unchanged regardless of size.
	big := strings.Repeat("z", defaultResultBudgetBytes*2)
	input, _ := json.Marshal(big)
	got := guardResult(input)
	if string(got) != string(input) {
		t.Error("scalar value should pass through unchanged")
	}
}

func TestGuardResult_ArrayUnchanged(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("a", defaultResultBudgetBytes+512)
	input, _ := json.Marshal([]string{big, big})
	got := guardResult(input)
	if string(got) != string(input) {
		t.Error("array value should pass through unchanged")
	}
}

func TestGuardResult_NullUnchanged(t *testing.T) {
	t.Parallel()
	input := json.RawMessage("null")
	got := guardResult(input)
	if string(got) != "null" {
		t.Errorf("null should pass through unchanged; got %s", got)
	}
}

// TestGuardResult_TruncationPreservesKeys verifies that non-string keys
// (bool, int, float) survive the truncation round-trip.
func TestGuardResult_TruncationPreservesKeys(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("h", defaultResultBudgetBytes+4096)
	input, err := json.Marshal(map[string]any{
		"url":          "https://example.com",
		"status":       float64(200),
		"content_type": "text/html",
		"truncated":    false,
		"markdown":     big,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := guardResult(input)
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if m["url"] != "https://example.com" {
		t.Errorf("url changed: %v", m["url"])
	}
	status, _ := m["status"].(float64)
	if status != 200 {
		t.Errorf("status changed: %v", m["status"])
	}
	if m["content_type"] != "text/html" {
		t.Errorf("content_type changed: %v", m["content_type"])
	}
	// The guard's truncation overrides the tool's truncated flag.
	if truncated, ok := m["truncated"].(bool); !ok || !truncated {
		t.Errorf("guard should set truncated=true; got %v", m["truncated"])
	}
	// markdown must be present (truncated).
	if _, ok := m["markdown"].(string); !ok {
		t.Error("markdown field missing after truncation")
	}
}

// TestGuardResult_ProducesValidJSONForHugePayload ensures the guard
// doesn't produce broken JSON even in edge cases.
func TestGuardResult_ProducesValidJSONForHugePayload(t *testing.T) {
	t.Parallel()
	// 8 MiB — well over budget.
	big := fmt.Sprintf("%064d", 0) + strings.Repeat("X", 8<<20)
	input, err := json.Marshal(map[string]any{"body": big})
	if err != nil {
		t.Fatal(err)
	}
	got := guardResult(input)
	if !json.Valid(got) {
		t.Fatalf("guard produced invalid JSON: %s", got[:min(200, len(got))])
	}
	if len(got) > defaultResultBudgetBytes+1024 {
		// Allow small overshoot for metadata.
		t.Errorf("truncated result too large: %d bytes", len(got))
	}
}

// ---------------------------------------------------------------------------
// Registry-level integration: bash foreground truncation
// ---------------------------------------------------------------------------

func TestRegistryBashForegroundTruncation(t *testing.T) {
	t.Parallel()
	reg := localBuild(t)

	// Generate a command that produces output exceeding the budget.
	res := callBash(t, reg, `for i in $(seq 1 20000); do echo "line $i"; done`, "Large output")

	// Verify the result is JSON-valid.
	if !json.Valid(res.ResultJSON) {
		t.Fatalf("result is not valid JSON: %s", string(res.ResultJSON)[:min(200, len(res.ResultJSON))])
	}

	var m map[string]any
	if err := json.Unmarshal(res.ResultJSON, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Must have truncation markers.
	truncated, _ := m["truncated"].(bool)
	notice, _ := m["truncation_notice"].(string)
	outFile, _ := m["output_file"].(string)

	if !truncated {
		t.Error("expected truncated=true on oversized bash foreground result")
	}
	if notice == "" {
		t.Error("expected non-empty truncation_notice")
	}
	if outFile == "" {
		t.Error("expected non-empty output_file on bash foreground result")
	}

	// output_file must point to the bash per-job log.
	if _, err := os.Stat(outFile); err != nil {
		t.Fatalf("output_file %q not accessible: %v", outFile, err)
	}
	full, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output_file: %v", err)
	}
	if !strings.Contains(string(full), "line 1") {
		t.Errorf("output_file should contain full output; got %d bytes (first 200: %q)",
			len(full), string(full)[:min(200, len(full))])
	}

	// output field must be truncated.
	output, _ := m["output"].(string)
	if len(output) >= len(full) {
		t.Errorf("output should be truncated: result output=%d bytes, full file=%d bytes", len(output), len(full))
	}

	// The result JSON must fit the budget.
	if len(res.ResultJSON) > defaultResultBudgetBytes+1024 {
		t.Errorf("truncated result still exceeds budget: %d bytes", len(res.ResultJSON))
	}
}

func TestRegistryBashForegroundSmallOutputUnchanged(t *testing.T) {
	t.Parallel()
	reg := localBuild(t)

	res := callBash(t, reg, `echo hello`, "Small output")

	var m map[string]any
	if err := json.Unmarshal(res.ResultJSON, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Small output must NOT carry truncation markers.
	if truncated, _ := m["truncated"].(bool); truncated {
		t.Error("small output should not have truncated=true")
	}
	if _, ok := m["truncation_notice"]; ok {
		t.Error("small output should not have truncation_notice")
	}
	// output_file is set on all bash results now (for guard integration),
	// so its presence alone is not a truncation signal.
	output, _ := m["output"].(string)
	if !strings.Contains(output, "hello") {
		t.Errorf("expected output to contain 'hello'; got %q", output)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// localBuild constructs a minimal registry with only local tools (no
// platform, no MCP).  Suitable for testing tool result shaping through
// the full Call → makeResult → guardResult pipeline.
func localBuild(t *testing.T) *Registry {
	t.Helper()
	lb := local.Build() // standard local bundle (without research, no LLM client)
	return Build(lb.Tools, nil, nil, nil)
}

// ---------------------------------------------------------------------------
// Registry-level integration: webfetch truncation
// ---------------------------------------------------------------------------

func TestRegistryWebFetchTruncation(t *testing.T) {
	t.Parallel()

	// Serve a large HTML page (simple repeated content).
	bigBody := "<html><body><p>" + strings.Repeat("paragraph text. ", 20000) + "</p></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(bigBody))
	}))
	defer srv.Close()

	reg := localBuild(t)

	args, err := json.Marshal(map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	res := reg.Call(context.Background(), "webfetch", args)
	if res.IsError {
		t.Fatalf("webfetch call error: %s", res.ErrMsg)
	}

	if !json.Valid(res.ResultJSON) {
		t.Fatalf("result is not valid JSON: %s", string(res.ResultJSON)[:min(200, len(res.ResultJSON))])
	}

	var m map[string]any
	if err := json.Unmarshal(res.ResultJSON, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	truncated, _ := m["truncated"].(bool)
	notice, _ := m["truncation_notice"].(string)
	outFile, _ := m["output_file"].(string)

	if !truncated {
		t.Error("expected truncated=true on oversized webfetch result")
	}
	if notice == "" {
		t.Error("expected non-empty truncation_notice")
	}
	if outFile == "" {
		t.Error("expected non-empty output_file on webfetch result")
	}

	// output_file must exist and contain the full content.
	full, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("output_file %q not readable: %v", outFile, err)
	}
	if !strings.Contains(string(full), "paragraph text") {
		t.Errorf("output_file should contain full markdown; got %d bytes", len(full))
	}

	// markdown field must be truncated.
	md, _ := m["markdown"].(string)
	if md == "" {
		t.Error("markdown field should be present (truncated)")
	}
	if len(md) >= len(full) {
		t.Errorf("markdown should be truncated: result=%d bytes, full=%d bytes", len(md), len(full))
	}

	// Result must fit within budget.
	if len(res.ResultJSON) > defaultResultBudgetBytes+1024 {
		t.Errorf("truncated result still exceeds budget: %d bytes", len(res.ResultJSON))
	}
}

func TestRegistryWebFetchSmallOutputUnchanged(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><h1>Hello</h1></body></html>"))
	}))
	defer srv.Close()

	reg := localBuild(t)
	args, err := json.Marshal(map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	res := reg.Call(context.Background(), "webfetch", args)
	if res.IsError {
		t.Fatalf("webfetch call error: %s", res.ErrMsg)
	}

	var m map[string]any
	if err := json.Unmarshal(res.ResultJSON, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Small output must NOT carry guard truncation markers.
	if truncated, _ := m["truncated"].(bool); truncated {
		t.Error("small webfetch should not have truncated=true (the 4 MiB cap truncated field is separate)")
	}
	if _, ok := m["truncation_notice"]; ok {
		t.Error("small webfetch should not have truncation_notice")
	}
	if _, ok := m["output_file"]; ok {
		t.Error("small webfetch should not have output_file")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// callBash is a small convenience that calls the bash tool through the
// registry and returns the CallResult.  Fatal on call error.
func callBash(t *testing.T, reg *Registry, command, summary string) CallResult {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"command":         command,
		"summary":         summary,
		"timeout_seconds": 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := reg.Call(context.Background(), "bash", args)
	if res.IsError {
		t.Fatalf("bash call error: %s", res.ErrMsg)
	}
	return res
}
