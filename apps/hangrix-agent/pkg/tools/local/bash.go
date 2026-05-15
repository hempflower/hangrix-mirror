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
	"time"
)

// bash runs commands in `sh -c`. The "interactive" niceties (login shell,
// rcfile, signal proxying) are deliberately absent — the agent is one
// process running in a one-shot container, so adding them would complicate
// the result without changing what the LLM can do.
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
	return "Run a shell command via 'sh -c'. Returns {stdout, stderr, exit_code, timed_out}. Set run_in_background=true for long-running commands; the response will include a task_id you can pass back via task_id to poll progress."
}
func (*bashTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":          map[string]any{"type": "string"},
			"working_dir":      map[string]any{"type": "string"},
			"timeout_seconds":  map[string]any{"type": "integer", "description": "Default 120."},
			"run_in_background": map[string]any{"type": "boolean"},
			"task_id":          map[string]any{"type": "string", "description": "Pass to poll a previously-started background task."},
		},
	}
}

func (b *bashTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	var a bashArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.TaskID != "" {
		return b.poll(a.TaskID), nil
	}
	if a.Command == "" {
		return nil, errors.New("command is required")
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

	cmd := exec.CommandContext(cctx, "sh", "-c", a.Command)
	if a.WorkingDir != "" {
		cmd.Dir = a.WorkingDir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
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
	cmd := exec.CommandContext(cctx, "sh", "-c", a.Command)
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

	if err := cmd.Start(); err != nil {
		job.exitCode = -1
		job.stderr.WriteString(err.Error())
		job.done = true
	} else {
		go func() {
			err := cmd.Wait()
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
		return &bashResult{ExitCode: -1, Stderr: fmt.Sprintf("unknown task_id: %s", id)}
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
