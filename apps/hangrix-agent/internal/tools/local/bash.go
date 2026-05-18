package local

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// bash runs commands in `bash -c`. The "interactive" niceties (login shell,
// rcfile, signal proxying) are deliberately absent — the agent is one
// process running in a one-shot container, so adding them would complicate
// the result without changing what the LLM can do. We require bash (not the
// POSIX `sh` fallback) because agent-authored scripts routinely lean on
// bashisms (pipefail, process substitution, `[[ … ]]`, arrays) and silently
// degrading to dash would produce surprising failures.
//
// Backgrounding is handled by spawning a goroutine, returning a taskId,
// and stashing the *exec.Cmd in a map so a follow-up bash call can poll
// it. The poller surfaces partial output even before the task ends, so
// the LLM doesn't have to wait until completion to see progress.

type bashArgs struct {
	Command          string `json:"command"`
	WorkingDir       string `json:"working_dir"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
	RunInBackground  bool   `json:"run_in_background"`
	TaskID           string `json:"task_id"` // poll an earlier background task
}

type bashResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
	TaskID   string `json:"task_id,omitempty"`
	Status   string `json:"status,omitempty"` // "running" | "done" | "" (sync result)
}

type bashJob struct {
	cmd      *exec.Cmd
	stdout   *bytes.Buffer
	stderr   *bytes.Buffer
	mu       sync.Mutex
	done     bool
	exitCode int
	timedOut bool
	cancel   context.CancelFunc
}

type bashTool struct {
	mu   sync.Mutex
	jobs map[string]*bashJob
}

func newBashTool() Tool { return &bashTool{jobs: map[string]*bashJob{}} }

func (*bashTool) Name() string { return "bash" }
func (*bashTool) Description() string {
	return "Run a shell command via 'bash -c' (bashisms like pipefail, process substitution, and [[ ]] are available). Returns {stdout, stderr, exit_code, timed_out}. " +
		"Set run_in_background=true to start a long-running command; the response will include a tool-generated task_id. " +
		"To check progress, call bash again with that task_id and no command — task_id is opaque, do not invent or modify it, and do not supply it together with command in the same call."
}
func (*bashTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":           map[string]any{"type": "string", "description": "Shell command to execute. Required unless task_id is given."},
			"working_dir":       map[string]any{"type": "string"},
			"timeout_seconds":   map[string]any{"type": "integer", "description": "Default 120."},
			"run_in_background": map[string]any{"type": "boolean", "description": "Start the command in the background; the response carries a tool-generated task_id you pass back to poll."},
			"task_id":           map[string]any{"type": "string", "description": "Opaque id returned by an earlier run_in_background=true call. Pass it back (with no command) to poll progress. Mutually exclusive with command — never supply both, and never invent a value."},
		},
	}
}

func (b *bashTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	var a bashArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.TaskID != "" {
		// task_id is the tool's contract for "poll an existing job." Mixing
		// it with a fresh command would conflate "start" and "poll" — the
		// LLM has no business inventing or recycling ids, so we refuse
		// instead of silently picking one branch.
		if a.Command != "" {
			return nil, errors.New("task_id and command are mutually exclusive: pass task_id (with no command) to poll an existing background task, or pass command to start a new one")
		}
		return b.poll(a.TaskID), nil
	}
	if a.Command == "" {
		return nil, errors.New("bash: missing 'command'. To start a new shell command, set 'command' (it runs via 'bash -c'). To poll a previously-started background task, set 'task_id' on its own (without 'command').")
	}
	if a.TimeoutSeconds <= 0 {
		a.TimeoutSeconds = 120
	}

	if a.RunInBackground {
		return b.spawnBackground(a), nil
	}
	return b.runForeground(ctx, a)
}

func (b *bashTool) runForeground(ctx context.Context, a bashArgs) (*bashResult, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(a.TimeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "bash", "-c", a.Command)
	if a.WorkingDir != "" {
		cmd.Dir = a.WorkingDir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Because Stdout/Stderr are *bytes.Buffer (not *os.File), exec wires up
	// internal pipes and Wait blocks until they EOF. If the LLM's script
	// backgrounds something (`./srv &`), the grandchild inherits those pipe
	// FDs and Wait hangs even after bash itself exits — looks like the tool
	// is "stuck" until the timeout. WaitDelay caps that drain window so we
	// return promptly with whatever output bash actually produced.
	cmd.WaitDelay = 2 * time.Second
	// Put bash + everything it spawns in its own process group so we can
	// signal the whole group as a unit. Without this, `./srv &` becomes an
	// orphan reparented to init when the agent process exits and lingers
	// inside the container.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// On context cancel/timeout, take down the group, not just bash.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	err := cmd.Run()
	// Even on the happy path, sweep the group: a successful `./srv &; exit 0`
	// leaves the grandchild alive and holding inherited FDs.
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	res := &bashResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if cctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
		} else if !res.TimedOut {
			// Non-exit errors (start failure, IO error) are surfaced as a
			// stderr line + non-zero exit so the LLM sees a uniform shape.
			res.Stderr = res.Stderr + "\n" + err.Error()
			res.ExitCode = -1
		} else {
			res.ExitCode = -1
		}
	}
	return res, nil
}

func (b *bashTool) spawnBackground(a bashArgs) *bashResult {
	id := newTaskID()
	// Background tasks live past the inbound context — they're meant to
	// outrun a single tool call. We hang them off context.Background()
	// + WithCancel so SIGTERM-driven shutdown can kill them.
	cctx, cancel := context.WithCancel(context.Background())
	if a.TimeoutSeconds > 0 {
		cctx, cancel = context.WithTimeout(cctx, time.Duration(a.TimeoutSeconds)*time.Second)
	}
	cmd := exec.CommandContext(cctx, "bash", "-c", a.Command)
	if a.WorkingDir != "" {
		cmd.Dir = a.WorkingDir
	}
	job := &bashJob{
		cmd:    cmd,
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		cancel: cancel,
	}
	cmd.Stdout = job.stdout
	cmd.Stderr = job.stderr
	// See runForeground: without WaitDelay, a script that spawns its own
	// background process keeps job.done=false forever because the
	// grandchild holds the inherited stdout/stderr pipe FDs.
	cmd.WaitDelay = 2 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	if err := cmd.Start(); err != nil {
		job.exitCode = -1
		job.stderr.WriteString(err.Error())
		job.done = true
	} else {
		go func() {
			err := cmd.Wait()
			// Sweep the group on completion so grandchildren don't outlive
			// the polled job. Safe to send to a finished group — kill
			// returns ESRCH and we ignore it.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			job.mu.Lock()
			defer job.mu.Unlock()
			if cctx.Err() == context.DeadlineExceeded {
				job.timedOut = true
			}
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					job.exitCode = exitErr.ExitCode()
				} else {
					job.exitCode = -1
				}
			}
			job.done = true
		}()
	}

	b.mu.Lock()
	b.jobs[id] = job
	b.mu.Unlock()

	return &bashResult{TaskID: id, Status: "running"}
}

func (b *bashTool) poll(id string) *bashResult {
	b.mu.Lock()
	job, ok := b.jobs[id]
	b.mu.Unlock()
	if !ok {
		return &bashResult{
			ExitCode: -1,
			Stderr: fmt.Sprintf(
				"bash: unknown task_id %q. Task ids are generated by the tool when you start a command with run_in_background=true and are valid for the lifetime of the agent process. Only pass back a task_id that bash previously returned in this session; do not invent or modify the value.",
				id,
			),
		}
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	res := &bashResult{
		Stdout:   job.stdout.String(),
		Stderr:   job.stderr.String(),
		ExitCode: job.exitCode,
		TimedOut: job.timedOut,
		TaskID:   id,
	}
	if job.done {
		res.Status = "done"
	} else {
		res.Status = "running"
	}
	return res
}

func newTaskID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "task_" + hex.EncodeToString(b[:])
}
