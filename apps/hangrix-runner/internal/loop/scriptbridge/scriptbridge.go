// Package scriptbridge implements the script step driver for workflow jobs.
// It generates a Node.js bootstrap from the embedded template, writes the
// user script and bootstrap into the container's workspace (via the host-side
// bind mount), executes `node bootstrap.mjs` via docker exec, and translates
// the result into the four-layer error model:
//
//  1. ConfigError           – YAML validation (server-side, not emitted here)
//  2. RuntimePrerequisiteError – node not found in container PATH
//  3. ScriptExecutionError  – JS exception / syntax error / Promise reject
//  4. PlatformApiError      – hangrix.* method returned non-2xx
//
// The caller is responsible for building the base environment (including
// HANGRIX_STEP_OUTPUT_FILE, WORKFLOW_TOKEN, platform vars), reading step
// outputs via docker exec cat after success, and forwarding stdout/stderr
// lines to the platform log.
package scriptbridge

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

//go:embed bootstrap.mjs
var bootstrapTemplate string

// ExecRunner abstracts the container exec interface so tests can inject a
// fake without dragging in the full orchestrator. Matches the signature of
// orchestrator.Orchestrator.Exec.
type ExecRunner func(ctx context.Context, containerID, workdir string, env map[string]string, args ...string) (ExecHandle, error)

// ExecHandle mirrors orchestrator.ExecHandle — the subset the driver needs.
type ExecHandle interface {
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() (int, error)
}

// Step describes one script step to execute.
type Step struct {
	ID     string
	Name   string
	Script string
	Env    map[string]string // per-step env overrides (already expanded)
	Dir    string
}

// Driver executes a script step inside a workflow container.
type Driver struct {
	// Exec is the container command runner (docker exec).
	Exec ExecRunner
	// HostWorkdir is the host-side path bind-mounted as /workspace in the
	// container. Script files are written here so they appear inside the
	// container without needing stdin piping through ExecHandle.
	HostWorkdir string
	// ContainerWorkdir is the container-side working directory (typically
	// "/workspace"). Script files are placed relative to this.
	ContainerWorkdir string
	// Log, when set, receives every stdout/stderr line from the script
	// process.  stream is "stdout" or "stderr".  When nil, lines are
	// drained and discarded.
	Log func(stream, line string)
}

// Result captures the outcome of a script step execution.
type Result struct {
	ExitCode int32
	// ErrorKind is one of "RuntimePrerequisiteError", "ScriptExecutionError",
	// "PlatformApiError", or "" on success.
	ErrorKind    string
	ErrorMessage string
	// ErrorDetails carries structured error info when available.
	ErrorDetails map[string]any
}

// Run executes a script step.
//
// baseEnv must include all platform runtime vars (HANGRIX_STEP_OUTPUT_FILE,
// HANGRIX_WORKFLOW_TOKEN, HANGRIX_PLATFORM_BASE_URL, etc.) — the caller
// builds this via WorkflowJobDriver.buildWorkflowEnv. stepOutputsJSON is the
// JSON-encoded map of accumulated step outputs for getStepOutputs().
//
// The driver adds HANGRIX_SCRIPT_STEP_USER_FILE (path to user-script.mjs
// inside the container) and HANGRIX_STEP_OUTPUTS_JSON.
func (d *Driver) Run(ctx context.Context, containerID string, step Step, baseEnv map[string]string, stepOutputsJSON string) Result {
	// 1. Check for node runtime in the container.
	if err := d.checkNode(ctx, containerID); err != nil {
		return Result{
			ExitCode:     -1,
			ErrorKind:    "RuntimePrerequisiteError",
			ErrorMessage: fmt.Sprintf("node runtime not found in container: %v", err),
		}
	}

	// 2. Prepare the script directory on the host side (under the bind mount).
	scriptDirHost, scriptDirContainer, err := d.prepareScriptDir(step)
	if err != nil {
		return Result{
			ExitCode:     -1,
			ErrorKind:    "RuntimePrerequisiteError",
			ErrorMessage: fmt.Sprintf("prepare script directory: %v", err),
		}
	}
	defer os.RemoveAll(scriptDirHost)

	// 3. Write user-script.mjs and bootstrap.mjs.
	if err := os.WriteFile(filepath.Join(scriptDirHost, "user-script.mjs"), []byte(step.Script), 0o644); err != nil {
		return Result{
			ExitCode:     -1,
			ErrorKind:    "RuntimePrerequisiteError",
			ErrorMessage: fmt.Sprintf("write user script: %v", err),
		}
	}
	if err := os.WriteFile(filepath.Join(scriptDirHost, "bootstrap.mjs"), []byte(bootstrapTemplate), 0o644); err != nil {
		return Result{
			ExitCode:     -1,
			ErrorKind:    "RuntimePrerequisiteError",
			ErrorMessage: fmt.Sprintf("write bootstrap: %v", err),
		}
	}

	// 4. Build the execution environment.
	execEnv := make(map[string]string, len(baseEnv)+len(step.Env)+2)
	for k, v := range baseEnv {
		execEnv[k] = v
	}
	for k, v := range step.Env {
		execEnv[k] = v
	}
	execEnv["HANGRIX_SCRIPT_STEP_USER_FILE"] = filepath.Join(scriptDirContainer, "user-script.mjs")
	if stepOutputsJSON != "" {
		execEnv["HANGRIX_STEP_OUTPUTS_JSON"] = stepOutputsJSON
	}

	// 5. Determine the working directory.
	workdir := d.ContainerWorkdir
	if workdir == "" {
		workdir = "/workspace"
	}
	if step.Dir != "" {
		if filepath.IsAbs(step.Dir) {
			workdir = step.Dir
		} else {
			workdir = filepath.Join(workdir, step.Dir)
		}
	}

	// 6. Execute: node <bootstrap.mjs>
	bootstrapContainer := filepath.Join(scriptDirContainer, "bootstrap.mjs")
	handle, err := d.Exec(ctx, containerID, workdir, execEnv, "node", bootstrapContainer)
	if err != nil {
		return Result{
			ExitCode:     -1,
			ErrorKind:    "RuntimePrerequisiteError",
			ErrorMessage: fmt.Sprintf("exec node: %v", err),
		}
	}

	// 7. Drain stdout/stderr. The bootstrap writes structured errors to
	//    stderr as JSON on failure, and user console.log to stdout.
	var stderrLines []string
	errs := make(chan error, 2)

	go func() {
		sc := bufio.NewScanner(handle.Stdout())
		sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
		for sc.Scan() {
			line := sc.Text()
			if d.Log != nil {
				d.Log("stdout", line)
			}
		}
		errs <- sc.Err()
	}()

	go func() {
		sc := bufio.NewScanner(handle.Stderr())
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		for sc.Scan() {
			line := sc.Text()
			stderrLines = append(stderrLines, line)
			if d.Log != nil {
				d.Log("stderr", line)
			}
		}
		errs <- sc.Err()
	}()

	for i := 0; i < 2; i++ {
		<-errs // drain scanner errors (non-fatal for classification)
	}

	exitCode, err := handle.Wait()
	if err != nil {
		return Result{
			ExitCode:     -1,
			ErrorKind:    "ScriptExecutionError",
			ErrorMessage: fmt.Sprintf("node wait: %v", err),
		}
	}

	// 8. Classify the result.
	if exitCode != 0 {
		kind, msg, details := classifyScriptError(stderrLines)
		return Result{
			ExitCode:     int32(exitCode),
			ErrorKind:    kind,
			ErrorMessage: msg,
			ErrorDetails: details,
		}
	}

	return Result{ExitCode: 0}
}

// checkNode verifies that node is available in the container PATH.
func (d *Driver) checkNode(ctx context.Context, containerID string) error {
	handle, err := d.Exec(ctx, containerID, "/", nil, "command", "-v", "node")
	if err != nil {
		return fmt.Errorf("exec command -v node: %w", err)
	}
	// Drain output.
	go func() {
		sc := bufio.NewScanner(handle.Stderr())
		for sc.Scan() {
		}
	}()
	sc := bufio.NewScanner(handle.Stdout())
	for sc.Scan() {
	}
	exitCode, err := handle.Wait()
	if err != nil {
		return fmt.Errorf("wait: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("node not found (exit %d)", exitCode)
	}
	return nil
}

// prepareScriptDir creates a temporary directory under the bind-mounted
// workspace. Returns both host-side and container-side paths.
func (d *Driver) prepareScriptDir(step Step) (hostDir, containerDir string, err error) {
	stepID := step.ID
	if stepID == "" {
		stepID = step.Name
	}
	if stepID == "" {
		stepID = "script"
	}
	stepID = sanitisePathSegment(stepID)

	containerDir = filepath.Join(d.ContainerWorkdir, ".hangrix-tmp", "script-step-"+stepID)
	hostDir = filepath.Join(d.HostWorkdir, ".hangrix-tmp", "script-step-"+stepID)

	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir: %w", err)
	}
	return hostDir, containerDir, nil
}

// sanitisePathSegment replaces characters unsafe for filesystem paths.
func sanitisePathSegment(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, s)
	if len(s) > 64 {
		s = s[:64]
	}
	if s == "" {
		s = "script"
	}
	return s
}

// classifyScriptError parses stderr lines to decide the error kind.
// The bootstrap writes structured JSON errors to stderr on failure:
//
//	{"error":"ScriptExecutionError","message":"...","stack":"..."}
//	{"error":"PlatformApiError","message":"...","code":"...","status":422,...}
func classifyScriptError(stderrLines []string) (kind, msg string, details map[string]any) {
	for i := len(stderrLines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(stderrLines[i])
		if line == "" || line[0] != '{' {
			continue
		}
		var parsed struct {
			Error   string         `json:"error"`
			Message string         `json:"message"`
			Code    string         `json:"code"`
			Status  int            `json:"status"`
			Details map[string]any `json:"details"`
		}
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			continue
		}
		if parsed.Error == "" {
			continue
		}
		kind = parsed.Error
		msg = parsed.Message
		if parsed.Code != "" || parsed.Status != 0 || parsed.Details != nil {
			details = map[string]any{}
			if parsed.Code != "" {
				details["code"] = parsed.Code
			}
			if parsed.Status != 0 {
				details["status"] = parsed.Status
			}
			if parsed.Details != nil {
				details["details"] = parsed.Details
			}
		}
		return
	}
	return "ScriptExecutionError", strings.Join(stderrLines, "\n"), nil
}
