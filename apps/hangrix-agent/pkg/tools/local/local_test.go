package local_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/tools/local"
)

// TestEditRequiresPriorRead pins the read-before-edit guard. The whole
// reason this exists is to stop the LLM from blindly mutating files it
// has never inspected — a regression here turns the agent into a hazard.
func TestEditRequiresPriorRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := byName(local.All())
	editTool := tools["edit"]
	readTool := tools["read"]

	// Edit before read: must refuse.
	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace", "find": "hello", "replace": "hi",
	}))
	if err == nil || !strings.Contains(err.Error(), "was not read") {
		t.Fatalf("expected read-first refusal, got %v", err)
	}

	// Read, then edit: must succeed.
	if _, err := readTool.Call(context.Background(), mustJSON(map[string]any{"path": path})); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace", "find": "hello", "replace": "hi",
	})); err != nil {
		t.Fatalf("edit after read: %v", err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "hi world" {
		t.Errorf("edit not applied: %q", string(body))
	}
}

// TestBashForeground covers the basic (sync) bash path: stdout/stderr
// capture and exit code propagation. Background mode + polling is
// covered by the runtime smoke test downstream.
func TestBashForeground(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	resRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command": "echo out; echo err 1>&2; exit 7",
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	// The bash result type is unexported by design — the IPC + LLM
	// round-trip is JSON, so we assert via JSON re-encode rather than
	// reaching into a private struct.
	out := mustReJSON(resRaw)
	var fields struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
		TimedOut bool   `json:"timed_out"`
	}
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("re-decode: %v (%s)", err, out)
	}
	if !strings.HasPrefix(fields.Stdout, "out") {
		t.Errorf("stdout = %q, want start with 'out'", fields.Stdout)
	}
	if !strings.HasPrefix(fields.Stderr, "err") {
		t.Errorf("stderr = %q, want start with 'err'", fields.Stderr)
	}
	if fields.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", fields.ExitCode)
	}
	if fields.TimedOut {
		t.Errorf("timed_out should be false")
	}
}

func byName(ts []local.Tool) map[string]local.Tool {
	m := map[string]local.Tool{}
	for _, t := range ts {
		m[t.Name()] = t
	}
	return m
}

func mustJSON(v any) json.RawMessage {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return out
}

func mustReJSON(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return out
}
