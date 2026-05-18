package local_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
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

	// Edit before read: must refuse, with an LLM-friendly message that
	// names the rule and the fix. The model uses the error text to
	// self-correct, so stripping the explanation regresses behaviour even
	// though the refusal itself still happens.
	_, err := editTool.Call(context.Background(), mustJSON(map[string]any{
		"path": path, "mode": "replace", "find": "hello", "replace": "hi",
	}))
	if err == nil || !strings.Contains(err.Error(), "was not read") {
		t.Fatalf("expected read-first refusal, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"'read' tool", "retry", "whitespace-sensitive"} {
		if !strings.Contains(msg, want) {
			t.Errorf("read-first error should mention %q (so the LLM knows how to recover); got: %s", want, msg)
		}
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

// TestBashTaskIDMutualExclusion pins the rule that task_id (poll an existing
// background task) and command (start a new one) cannot coexist in a single
// call. The LLM has no business inventing task_id values, so we refuse the
// call rather than silently picking one branch.
func TestBashTaskIDMutualExclusion(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	_, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command": "echo hi",
		"task_id": "task_deadbeef",
	}))
	if err == nil {
		t.Fatal("expected error when both command and task_id are supplied")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should explain the conflict, got: %v", err)
	}
}

// TestGlobRespectsGitignore pins the two filters layered on top of the
// raw walker: `.git/` is never returned, and paths matched by `.gitignore`
// are dropped. Both matter because an agent globbing `**/*` in a real
// repo otherwise drowns in `.git/objects/...` and `node_modules/...`
// noise, burning context on files the human didn't author. The matcher
// is pure Go (go-gitignore), so this test doesn't need a real git binary
// — we just stub out a `.git/` directory so the matcher anchors at the
// temp dir.
func TestGlobRespectsGitignore(t *testing.T) {
	dir := t.TempDir()
	// Layout:
	//   keep.txt              — should appear
	//   ignored.txt           — listed in .gitignore, should NOT appear
	//   sub/keep.txt          — should appear
	//   ignored-dir/skip.txt  — under an ignored directory, should NOT appear
	//   .git/HEAD             — never returned regardless of .gitignore
	mustWrite := func(rel, body string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(".gitignore", "ignored.txt\nignored-dir/\n")
	mustWrite("keep.txt", "k")
	mustWrite("ignored.txt", "x")
	mustWrite("sub/keep.txt", "k")
	mustWrite("ignored-dir/skip.txt", "x")
	mustWrite(".git/HEAD", "ref: refs/heads/main\n")

	// loadGitignore walks up cwd looking for a `.git` marker; switching
	// cwd into the temp dir anchors the matcher there.
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	tools := byName(local.All())
	glob := tools["glob"]
	res, err := glob.Call(context.Background(), mustJSON(map[string]any{"pattern": "**/*"}))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	out := mustReJSON(res)
	var fields struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	got := map[string]bool{}
	for _, p := range fields.Paths {
		got[filepath.ToSlash(p)] = true
	}
	for _, want := range []string{"keep.txt", "sub/keep.txt"} {
		if !got[want] {
			t.Errorf("expected glob to return %q; got %v", want, fields.Paths)
		}
	}
	for _, banned := range []string{"ignored.txt", "ignored-dir/skip.txt", ".git/HEAD"} {
		if got[banned] {
			t.Errorf("glob should have filtered %q (gitignored / inside .git); got %v", banned, fields.Paths)
		}
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
