package scriptbridge

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// fakeExecRunner returns a pre-built fake handle for testing.
type fakeExecRunner struct {
	handle ExecHandle
}

func (f fakeExecRunner) call(ctx context.Context, containerID, workdir string, env map[string]string, args ...string) (ExecHandle, error) {
	return f.handle, nil
}

// fakeExecHandle implements ExecHandle for tests.
type fakeExecHandle struct {
	stdoutData []byte
	stderrData []byte
	exitCode   int
}

func (h *fakeExecHandle) Stdout() io.ReadCloser { return &byteReadCloser{data: h.stdoutData} }
func (h *fakeExecHandle) Stderr() io.ReadCloser { return &byteReadCloser{data: h.stderrData} }
func (h *fakeExecHandle) Wait() (int, error) { return h.exitCode, nil }

type byteReadCloser struct {
	data   []byte
	offset int
}

func (b *byteReadCloser) Read(p []byte) (int, error) {
	if b.offset >= len(b.data) {
		return 0, nil // io.EOF-like — scanner handles this
	}
	n := copy(p, b.data[b.offset:])
	b.offset += n
	if b.offset >= len(b.data) {
		return n, nil
	}
	return n, nil
}

func (b *byteReadCloser) Close() error { return nil }

func TestSanitisePathSegment(t *testing.T) {
	longName := "very-long-name-" + string(make([]byte, 80-15))
	for i := range longName[15:] {
		longName = longName[:15+i] + "x"
	}
	// Fix: just construct the strings directly
	longIn := "very-long-name-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	longWant := "very-long-name-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

	tests := []struct {
		in, want string
	}{
		{"hello-world", "hello-world"},
		{"hello world!", "hello_world_"},
		{"a/b\\c:d", "a_b_c_d"},
		{"", "script"},
		{longIn, longWant},
	}
	for _, tt := range tests {
		got := sanitisePathSegment(tt.in)
		if got != tt.want {
			t.Errorf("sanitisePathSegment(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClassifyScriptError_ScriptExecutionError(t *testing.T) {
	lines := []string{
		`ReferenceError: foo is not defined`,
		`    at file:///workspace/.hangrix-tmp/script-step-notify/user-script.mjs:3:1`,
		`{"error":"ScriptExecutionError","message":"foo is not defined","stack":"..."}`,
	}
	kind, msg, _ := classifyScriptError(lines)
	if kind != "ScriptExecutionError" {
		t.Errorf("kind = %q, want ScriptExecutionError", kind)
	}
	if msg != "foo is not defined" {
		t.Errorf("msg = %q, want 'foo is not defined'", msg)
	}
}

func TestClassifyScriptError_PlatformApiError(t *testing.T) {
	lines := []string{
		`{"error":"PlatformApiError","message":"issue.comment failed: 422 validation_failed: content is required","code":"validation_failed","status":422}`,
	}
	kind, msg, details := classifyScriptError(lines)
	if kind != "PlatformApiError" {
		t.Errorf("kind = %q, want PlatformApiError", kind)
	}
	if msg != "issue.comment failed: 422 validation_failed: content is required" {
		t.Errorf("msg = %q", msg)
	}
	if details["code"] != "validation_failed" {
		t.Errorf("code = %v, want validation_failed", details["code"])
	}
	if details["status"] != 422 {
		t.Errorf("status = %v (type %T), want 422", details["status"], details["status"])
	}
}

func TestClassifyScriptError_NoStructuredError(t *testing.T) {
	lines := []string{
		"some random stderr output",
		"node:internal/errors:496",
	}
	kind, msg, _ := classifyScriptError(lines)
	if kind != "ScriptExecutionError" {
		t.Errorf("kind = %q, want ScriptExecutionError", kind)
	}
	if msg != "some random stderr output\nnode:internal/errors:496" {
		t.Errorf("msg = %q", msg)
	}
}

func TestClassifyScriptError_EmptyStderr(t *testing.T) {
	kind, msg, _ := classifyScriptError(nil)
	if kind != "ScriptExecutionError" {
		t.Errorf("kind = %q, want ScriptExecutionError", kind)
	}
	if msg != "" {
		t.Errorf("msg = %q, want empty", msg)
	}
}

func TestPrepareScriptDir(t *testing.T) {
	tmp := t.TempDir()
	drv := &Driver{
		HostWorkdir:      tmp,
		ContainerWorkdir: "/workspace",
	}
	step := Step{ID: "notify", Name: "Notify", Script: "console.log(1)"}

	hostDir, containerDir, err := drv.prepareScriptDir(step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantHost := filepath.Join(tmp, ".hangrix-tmp", "script-step-notify")
	wantContainer := "/workspace/.hangrix-tmp/script-step-notify"

	if hostDir != wantHost {
		t.Errorf("hostDir = %q, want %q", hostDir, wantHost)
	}
	if containerDir != wantContainer {
		t.Errorf("containerDir = %q, want %q", containerDir, wantContainer)
	}

	// Verify the directory was created.
	if info, err := os.Stat(hostDir); err != nil || !info.IsDir() {
		t.Errorf("hostDir not created: stat err=%v", err)
	}
}

func TestPrepareScriptDir_FallbackToName(t *testing.T) {
	tmp := t.TempDir()
	drv := &Driver{
		HostWorkdir:      tmp,
		ContainerWorkdir: "/workspace",
	}
	step := Step{Name: "Send notification", Script: "console.log(1)"}

	_, containerDir, err := drv.prepareScriptDir(step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "/workspace/.hangrix-tmp/script-step-Send_notification"
	if containerDir != want {
		t.Errorf("containerDir = %q, want %q", containerDir, want)
	}
}

func TestPrepareScriptDir_FallbackToScript(t *testing.T) {
	tmp := t.TempDir()
	drv := &Driver{
		HostWorkdir:      tmp,
		ContainerWorkdir: "/workspace",
	}
	step := Step{Script: "console.log(1)"} // no ID, no Name

	_, containerDir, err := drv.prepareScriptDir(step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "/workspace/.hangrix-tmp/script-step-script"
	if containerDir != want {
		t.Errorf("containerDir = %q, want %q", containerDir, want)
	}
}
