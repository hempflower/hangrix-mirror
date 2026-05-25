package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

// LocalOrchestrator runs the agent binary as a direct subprocess without
// Docker. It implements the full Orchestrator interface so the runner's
// session driver can operate unchanged — only the "how do we start a
// process" layer is replaced.
//
// In mock/local mode there are no long-lived containers: every Start() call
// spawns a fresh hangrix-agent process. The returned Handle carries a
// synthetic container ID that the runner persists via SetContainer so the
// resume path (task.ContainerID != "") still bypasses the initial clone.
//
// IMPORTANT: LocalOrchestrator is intended ONLY for session execution
// (SessionDriver). WorkflowJobDriver must continue to use the Docker
// orchestrator even in mock mode — the runner wires LocalOrchestrator into
// Loop.SessionOrchestrator, not Loop.Orchestrator, so workflow jobs keep
// their Docker/container/image/volume contract.
type LocalOrchestrator struct {
	mu          sync.Mutex
	nextLocalID int64
	removed     []string
}

// NewLocal creates a LocalOrchestrator ready for use. It has no external
// dependencies beyond the agent binary on disk and a writable workdir.
func NewLocal() *LocalOrchestrator {
	return &LocalOrchestrator{}
}

func (o *LocalOrchestrator) Start(ctx context.Context, t Task) (Handle, error) {
	if t.AgentBinaryPath == "" {
		return nil, fmt.Errorf("AgentBinaryPath is required")
	}
	info, err := os.Stat(t.AgentBinaryPath)
	if err != nil {
		return nil, fmt.Errorf("agent binary not found at %s: %w", t.AgentBinaryPath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("agent binary at %s is not a regular file (mode=%s)", t.AgentBinaryPath, info.Mode())
	}
	if t.HostWorkdir == "" {
		return nil, fmt.Errorf("HostWorkdir is required")
	}
	if err := os.MkdirAll(t.HostWorkdir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure workdir %s: %w", t.HostWorkdir, err)
	}

	// Build a synthetic container ID. On the first trigger (ContainerID
	// empty) the runner persists this back to the platform; on resume the
	// task carries the same ID and the SessionDriver skips the clone.
	cid := t.ContainerID
	if cid == "" {
		cid = fmt.Sprintf("local-session-%d", t.SessionID)
	}

	// Run the agent binary directly with its environment.
	// Only the runner-constructed task environment is passed —
	// matching the Docker session contract where the container
	// starts with exactly the task env and nothing else.
	cmd := exec.CommandContext(ctx, t.AgentBinaryPath)
	cmd.Dir = t.HostWorkdir
	cmd.Env = make([]string, 0, len(t.Env)+1)
	for k, v := range t.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if t.HostAddendumPath != "" {
		cmd.Env = append(cmd.Env, "HANGRIX_HOST_ADDENDUM="+t.HostAddendumPath)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start agent: %w", err)
	}

	return &localHandle{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr, containerID: cid}, nil
}

// RemoveContainer is a no-op in local mode — there is no Docker container
// to remove. Cleanup acknowledgements still succeed so the platform's
// cleanup sweeper doesn't stall.
func (o *LocalOrchestrator) RemoveContainer(_ context.Context, id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.removed = append(o.removed, id)
	return nil
}

// WorkflowContainer returns a synthetic container ID. In local mode there
// is no Docker sandbox; Exec runs step commands directly on the host.
//
// NOTE: this method exists only to satisfy the Orchestrator interface.
// LocalOrchestrator is wired into Loop.SessionOrchestrator, not
// Loop.Orchestrator, so WorkflowContainer and Exec are never called on
// a LocalOrchestrator in production. They remain here for interface
// compliance and testability.
func (o *LocalOrchestrator) WorkflowContainer(_ context.Context, image string, _ *BuildSpec, _ []string, _ string, _ map[string]string, _ []Volume) (string, error) {
	if image == "" {
		return "", fmt.Errorf("orchestrator: image is required")
	}
	id := atomic.AddInt64(&o.nextLocalID, 1)
	return fmt.Sprintf("local-wf-container-%d", id), nil
}

// Exec runs a command directly as a subprocess. Stdout and stderr are
// streamed via the returned ExecHandle's pipes; the caller drains them and
// calls Wait to collect the exit code.
//
// NOTE: same as WorkflowContainer — this is never called via the production
// wiring path (see WorkflowContainer doc).
func (o *LocalOrchestrator) Exec(ctx context.Context, containerID, workdir string, env map[string]string, args ...string) (ExecHandle, error) {
	if containerID == "" {
		return nil, fmt.Errorf("orchestrator: containerID is required")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = workdir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start exec: %w", err)
	}

	return &localExecHandle{cmd: cmd, stdout: stdout, stderr: stderr}, nil
}

// localHandle implements Handle for a directly-spawned agent process.
type localHandle struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.Reader
	stderr      io.Reader
	containerID string
}

func (h *localHandle) Stdin() io.WriteCloser { return h.stdin }
func (h *localHandle) Stdout() io.Reader     { return h.stdout }
func (h *localHandle) Stderr() io.Reader     { return h.stderr }
func (h *localHandle) ContainerID() string   { return h.containerID }
func (h *localHandle) Wait() (int, error) {
	err := h.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return -1, err
}
func (h *localHandle) Stop(ctx context.Context) error {
	if h.cmd.Process != nil {
		// Best-effort SIGTERM; the process may already be exiting.
		_ = h.cmd.Process.Signal(os.Interrupt)
	}
	return nil
}

// localExecHandle implements ExecHandle backed by a direct subprocess.
type localExecHandle struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr io.ReadCloser
	mu     sync.Mutex
	done   bool
}

func (h *localExecHandle) Stdout() io.ReadCloser { return h.stdout }
func (h *localExecHandle) Stderr() io.ReadCloser { return h.stderr }
func (h *localExecHandle) Wait() (int, error) {
	h.mu.Lock()
	if h.done {
		h.mu.Unlock()
		return 0, nil
	}
	h.mu.Unlock()

	err := h.cmd.Wait()
	h.mu.Lock()
	h.done = true
	h.mu.Unlock()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return -1, err
}
