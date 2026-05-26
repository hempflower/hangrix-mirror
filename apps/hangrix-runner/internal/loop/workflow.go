package loop

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/loop/scriptbridge"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
)

// WorkflowJobDriver runs a single workflow job to completion. Unlike
// SessionDriver, it does NOT run the agent binary, does NOT create
// long-lived reusable containers, and does NOT participate in the
// agent message timeline. Each job gets:
//
//  1. A one-time working directory under WorkspaceRoot
//  2. An independent git checkout at the job's specified ref/sha
//  3. A fresh container (sleep infinity) for the duration of the job
//  4. Sequential step execution via docker exec bash -lc <run>
//  5. Line-by-line log forwarding to the platform
//  6. Container cleanup on completion
//
// Concurrency: one driver per job; no shared state with other workers.
type WorkflowJobDriver struct {
	Client        *client.Client
	Orchestrator  orchestrator.Orchestrator
	WorkspaceRoot string
	BaseURL       string
}

// Run executes the workflow job and returns nil on success. Any error
// returned means the job infrastructure itself failed (checkout, container
// create, etc.) — step-level failures are reported via terminate and do
// NOT surface as a Go error here.
func (d *WorkflowJobDriver) Run(ctx context.Context, job *client.WorkflowJob) error {
	if job == nil {
		return fmt.Errorf("workflow job is nil")
	}

	// 1. Signal the platform we're starting this job.
	if err := d.Client.MarkWorkflowJobRunning(ctx, job.JobRunID); err != nil {
		log.Printf("workflow job %d: mark running: %v", job.JobRunID, err)
		// Continue anyway — the platform will eventually time-out if we
		// silently die, but surfacing the log gives the operator a clue.
	}

	// 2. Prepare a one-time working directory.
	hostWorkdir := filepath.Join(d.WorkspaceRoot, fmt.Sprintf("wf-job-%d", job.JobRunID))
	repoCheckout := filepath.Join(hostWorkdir, "repo")
	if err := os.MkdirAll(hostWorkdir, 0o755); err != nil {
		return d.fail(ctx, job, fmt.Errorf("mkdir workdir: %w", err))
	}
	defer func() {
		if err := os.RemoveAll(hostWorkdir); err != nil {
			log.Printf("workflow job %d: cleanup workdir: %v", job.JobRunID, err)
		}
	}()

	// 3. Clone the host repo and checkout the target ref/sha.
	if job.Owner != "" && job.Name != "" && job.CommitSHA != "" {
		cloneJob := cloneSpec{
			BaseURL:       d.BaseURL,
			Owner:         job.Owner,
			Name:          job.Name,
			SessionToken:  "",                // workflow jobs don't use session tokens
			WorkflowToken: job.WorkflowToken, // authenticates the clone of a private host repo
			Dest:          repoCheckout,
			// For workflow jobs we always check out a specific ref.
			// We clone the repo with --no-checkout, then checkout
			// the target commit.
		}
		if err := d.cloneWorkflowRepo(ctx, cloneJob, job.CommitSHA); err != nil {
			return d.fail(ctx, job, fmt.Errorf("clone repo: %w", err))
		}
	} else {
		// No repo info — create an empty directory. Some workflow jobs
		// may be repo-agnostic (future use case).
		if err := os.MkdirAll(repoCheckout, 0o755); err != nil {
			return d.fail(ctx, job, fmt.Errorf("mkdir repo: %w", err))
		}
	}

	// 4. Resolve the container image (pull or build).
	image := job.Container.Image
	if image == "" {
		return d.fail(ctx, job, fmt.Errorf("container image is required"))
	}
	var buildSpec *orchestrator.BuildSpec
	if job.Container.Build != nil {
		buildSpec = &orchestrator.BuildSpec{
			Dockerfile: job.Container.Build.Dockerfile,
			Context:    job.Container.Build.Context,
			Args:       job.Container.Build.Args,
		}
	}

	// 5. Early validation: expand ${VAR_NAME} references against repo
	//    variables. The actual expansion for step execution happens in
	//    buildWorkflowEnv, but we validate here so we can fail fast
	//    before spending time on container creation and checkout.
	//
	//    We expand a temporary copy so the early check doesn't mutate
	//    job.Container.Env in place (buildWorkflowEnv does its own
	//    expansion on a fresh copy at exec time).
	{
		tmp := make(map[string]string, len(job.Container.Env))
		for k, v := range job.Container.Env {
			tmp[k] = v
		}
		if err := expandEnv(tmp, job.RepoVariables); err != nil {
			return d.fail(ctx, job, fmt.Errorf("expand env: %w", err))
		}
	}

	// 6. Create the workflow container.
	containerID, err := d.Orchestrator.WorkflowContainer(
		ctx,
		image,
		buildSpec,
		job.Container.Entrypoint,
		repoCheckout,
		job.Container.Env, // validated above; may be unused by impl
		orchestratorVolumes(job.Container.Volumes, job.RepoID),
	)
	if err != nil {
		return d.fail(ctx, job, fmt.Errorf("create container: %w", err))
	}
	defer func() {
		if err := d.Orchestrator.RemoveContainer(context.Background(), containerID); err != nil {
			log.Printf("workflow job %d: remove container %s: %v", job.JobRunID, containerID, err)
		}
	}()

	// 7. Ensure /tmp/hangrix exists inside the container for step output files.
	if err := d.ensureStepOutputDir(ctx, containerID); err != nil {
		log.Printf("workflow job %d: ensure /tmp/hangrix: %v", job.JobRunID, err)
		// Non-fatal: steps can still run, but output capture may fail.
	}

	// 8. Execute steps sequentially.
	workingDir := job.WorkingDir
	if workingDir == "" {
		workingDir = "/workspace"
	}
	timeout := time.Duration(job.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 60 * time.Minute
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Accumulated step outputs from previous steps, indexed by step ID
	// (or 1-based index fallback when step has no ID).
	// Shape: stepID -> {key -> value}
	stepOutputs := make(map[string]map[string]string)

	for i, step := range job.Steps {
		stepName := step.Name
		if stepName == "" {
			switch step.Type {
			case "release":
				stepName = fmt.Sprintf("release %s", withString(step.With, "tag"))
			case "script":
				stepName = "script"
			default:
				stepName = step.Run
			}
		}
		log.Printf("workflow job %d: step %d/%d %q", job.JobRunID, i+1, len(job.Steps), stepName)

		if step.Type == "release" {
			// ---- release step: call the platform release API ----
			outputs, err := d.runReleaseStep(stepCtx, job, step, i, stepOutputs, repoCheckout)
			if err != nil {
				msg := fmt.Sprintf("step %q: %v", stepName, err)
				d.appendSystemLog(ctx, job.JobRunID, msg)
				d.reportStepResult(ctx, job, step, i, -1, nil, nil)
				d.terminate(ctx, job, "failed", -1, msg)
				return nil
			}
			// Index outputs for subsequent step interpolation.
			// runReleaseStep already called reportStepResult on success.
			stepID := step.ID
			if stepID == "" {
				stepID = fmt.Sprintf("%d", i+1)
			}
			if len(outputs) > 0 {
				stepOutputs[stepID] = outputs
			}
			continue
		}

		if step.Type == "script" {
			// ---- script step: execute via Node.js with hangrix SDK ----
			outputs, err := d.runScriptStep(stepCtx, job, containerID, workingDir, step, i, stepOutputs, repoCheckout)
			if err != nil {
				msg := fmt.Sprintf("step %q: %v", stepName, err)
				d.appendSystemLog(ctx, job.JobRunID, msg)
				d.reportStepResult(ctx, job, step, i, -1, nil, nil)
				d.terminate(ctx, job, "failed", -1, msg)
				return nil
			}
			// Index outputs for subsequent step interpolation.
			stepID := step.ID
			if stepID == "" {
				stepID = fmt.Sprintf("%d", i+1)
			}
			if len(outputs) > 0 {
				stepOutputs[stepID] = outputs
			}
			continue
		}

		// ---- run step: docker exec bash -lc <run> ----

		// Expand ${{ steps.<id>.outputs.<key> }} references using
		// outputs captured from previous steps. Fails the job when
		// a referenced step id or output key is not found.
		expanded, err := expandStepOutputRefs(step.Run, stepOutputs)
		if err != nil {
			msg := fmt.Sprintf("step %q: %v", stepName, err)
			d.appendSystemLog(ctx, job.JobRunID, msg)
			d.reportStepResult(ctx, job, step, i, -1, nil, nil)
			d.terminate(ctx, job, "failed", -1, msg)
			return nil
		}
		step.Run = expanded

		exitCode, err := d.runStep(stepCtx, job, containerID, workingDir, step, i)
		if err != nil {
			// Infrastructure failure (exec failed, context cancelled, etc.)
			d.appendSystemLog(ctx, job.JobRunID, fmt.Sprintf("step %q: %v", stepName, err))
			d.reportStepResult(ctx, job, step, i, -1, nil, nil)
			d.terminate(ctx, job, "failed", -1, err.Error())
			return nil // step error already reported; not a driver error
		}
		if exitCode != 0 {
			msg := fmt.Sprintf("step %q exited with code %d", stepName, exitCode)
			d.appendSystemLog(ctx, job.JobRunID, msg)
			d.reportStepResult(ctx, job, step, i, exitCode, nil, nil)
			d.terminate(ctx, job, "failed", exitCode, msg)
			return nil // step failure already reported
		}

		// Capture and report step outputs on success.
		outputs := d.captureAndReportStepOutputs(ctx, job, containerID, step, i)
		// Index outputs for subsequent step interpolation.
		stepID := step.ID
		if stepID == "" {
			stepID = fmt.Sprintf("%d", i+1)
		}
		if len(outputs) > 0 {
			stepOutputs[stepID] = outputs
		}
	}

	// 9. Success.
	d.terminate(ctx, job, "success", 0, "")
	return nil
}

// runStep executes a single workflow step inside the container via
// docker exec bash -lc <run>. It streams stdout/stderr line-by-line
// to the platform and returns the exit code.
//
// stepIndex (0-based) is used to derive the step output file path
// when the step has no explicit ID.
func (d *WorkflowJobDriver) runStep(ctx context.Context, job *client.WorkflowJob, containerID, workingDir string, step client.WorkflowStep, stepIndex int) (int32, error) {
	// Build the runtime env for this step.
	stepEnv, err := d.buildWorkflowEnv(job)
	if err != nil {
		return -1, fmt.Errorf("build env: %w", err)
	}

	// Per-step env overrides the job/container env. Expand ${VAR}
	// references against repo variables the same way job env is expanded,
	// then merge over stepEnv so a step's own value wins.
	if len(step.Env) > 0 {
		se := make(map[string]string, len(step.Env))
		for k, v := range step.Env {
			se[k] = v
		}
		if err := expandEnv(se, job.RepoVariables); err != nil {
			return -1, fmt.Errorf("expand step env: %w", err)
		}
		for k, v := range se {
			stepEnv[k] = v
		}
	}

	// Inject HANGRIX_STEP_OUTPUT_FILE so the step knows where to write
	// key=value outputs. Use the step's explicit ID when present,
	// otherwise fall back to the 1-based step index. Set after the
	// step-env merge so a step can't accidentally clobber it.
	stepOutputFile := stepOutputPath(step, stepIndex)
	stepEnv["HANGRIX_STEP_OUTPUT_FILE"] = stepOutputFile

	// A step may override the job working directory. Relative paths
	// resolve against the job working directory (e.g. "apps/hangrix"
	// under /workspace); absolute paths are used as-is.
	stepWorkingDir := workingDir
	if step.Dir != "" {
		if filepath.IsAbs(step.Dir) {
			stepWorkingDir = step.Dir
		} else {
			stepWorkingDir = filepath.Join(workingDir, step.Dir)
		}
	}

	handle, err := d.Orchestrator.Exec(ctx, containerID, stepWorkingDir, stepEnv, "bash", "-lc", step.Run)
	if err != nil {
		return -1, fmt.Errorf("exec step: %w", err)
	}

	// Drain stdout and stderr concurrently, forwarding each line as a log.
	// We use goroutines + channels to interleave lines in arrival order
	// without head-of-line blocking between the two streams.
	type logLine struct {
		stream string
		line   string
	}
	lines := make(chan logLine, 64)
	errs := make(chan error, 2)

	drain := func(stream string, r *bufio.Scanner) {
		for r.Scan() {
			lines <- logLine{stream: stream, line: r.Text()}
		}
		if err := r.Err(); err != nil {
			errs <- fmt.Errorf("%s scanner: %w", stream, err)
		} else {
			errs <- nil
		}
	}

	stdoutScanner := bufio.NewScanner(handle.Stdout())
	stdoutScanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	stderrScanner := bufio.NewScanner(handle.Stderr())
	stderrScanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

	go drain("stdout", stdoutScanner)
	go drain("stderr", stderrScanner)

	// Forward log lines until both scanners are done.
	drainers := 2
	for drainers > 0 {
		select {
		case l := <-lines:
			d.appendLog(ctx, job.JobRunID, l.stream, l.line)
		case err := <-errs:
			drainers--
			if err != nil {
				// Scanner error — drain remaining lines, then return.
				go func() {
					for range lines {
					}
				}()
				// Wait for the other scanner to finish to avoid leaking
				// goroutines, then return the error.
				for drainers > 0 {
					<-errs
					drainers--
				}
				return -1, err
			}
		case <-ctx.Done():
			// Timeout or cancellation — drain remaining scanners.
			go func() {
				for range lines {
				}
			}()
			for drainers > 0 {
				<-errs
				drainers--
			}
			return -1, ctx.Err()
		}
	}

	exitCode, err := handle.Wait()
	if err != nil {
		return -1, fmt.Errorf("wait: %w", err)
	}
	return int32(exitCode), nil
}

// buildWorkflowEnv constructs the platform runtime env vars injected into
// each step's exec call. These follow the design doc's env merge order:
//
//	container.env  ←  workflow.env  ←  job.env  ←  platform runtime env
//
// The merged container/workflow/job env is already in job.Container.Env
// (the server does the merge). This function adds the platform runtime
// vars that the runner owns.
func (d *WorkflowJobDriver) buildWorkflowEnv(job *client.WorkflowJob) (map[string]string, error) {
	env := make(map[string]string)
	for k, v := range job.Container.Env {
		env[k] = v
	}
	// Expand ${VAR_NAME} references against repo variables before
	// injecting platform runtime vars. The expandEnv call in Run is an
	// early validation gate; this one produces the actual expanded values
	// that bash will see at exec time.
	if err := expandEnv(env, job.RepoVariables); err != nil {
		return nil, err
	}
	// Platform runtime env — injected by runner, not server.
	env["HANGRIX_WORKFLOW_RUN_ID"] = strconv.FormatInt(job.WorkflowRunID, 10)
	env["HANGRIX_WORKFLOW_NAME"] = job.WorkflowName
	env["HANGRIX_WORKFLOW_JOB_KEY"] = job.JobKey
	env["HANGRIX_REPO_OWNER"] = job.Owner
	env["HANGRIX_REPO_NAME"] = job.Name
	if job.CommitSHA != "" {
		env["HANGRIX_COMMIT_SHA"] = job.CommitSHA
	}
	if job.CheckoutRef != "" {
		env["HANGRIX_CHECKOUT_REF"] = job.CheckoutRef
	}
	if job.EventName != "" {
		env["HANGRIX_EVENT_NAME"] = job.EventName
	}
	if job.Tag != "" {
		env["HANGRIX_TAG"] = job.Tag
	}
	if job.EventCauseID != "" {
		env["HANGRIX_EVENT_CAUSE_ID"] = job.EventCauseID
	}
	// Platform API URL and workflow-scoped token for API calls from
	// within workflow steps (e.g. creating releases via curl).
	env["HANGRIX_PLATFORM_BASE_URL"] = d.BaseURL
	env["HANGRIX_WORKFLOW_TOKEN"] = job.WorkflowToken
	// Trigger actor identity — who or what initiated this workflow run.
	// These are injected so steps can attribute side effects (comments,
	// releases, etc.) to the correct actor. Per the design doc, the
	// trigger actor is distinct from the run actor (workflow:run:<id>)
	// that the platform uses for subsequent step side effects.
	if job.TriggerActorKind != "" {
		env["HANGRIX_TRIGGER_ACTOR_KIND"] = job.TriggerActorKind
	}
	if job.TriggerActorID != "" {
		env["HANGRIX_TRIGGER_ACTOR_ID"] = job.TriggerActorID
	}
	if job.TriggerActorDisplayName != "" {
		env["HANGRIX_TRIGGER_ACTOR_DISPLAY_NAME"] = job.TriggerActorDisplayName
	}
	// Dispatch inputs already transformed to WORKFLOW_INPUT_* keys by
	// the server; inject them as-is so steps can use $WORKFLOW_INPUT_REF etc.
	for k, v := range job.Inputs {
		env[k] = v
	}
	return env, nil
}

// appendLog sends one stdout/stderr line to the platform log endpoint.
func (d *WorkflowJobDriver) appendLog(ctx context.Context, jobRunID int64, stream, line string) {
	if err := d.Client.AppendWorkflowJobLog(ctx, jobRunID, stream, line); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("workflow job %d: append log (%s): %v", jobRunID, stream, err)
	}
}

// appendSystemLog sends a system-level log line (not tied to a step's
// stdout/stderr).
func (d *WorkflowJobDriver) appendSystemLog(ctx context.Context, jobRunID int64, msg string) {
	d.appendLog(ctx, jobRunID, "system", msg)
}

// terminate reports the job's final status to the platform.
func (d *WorkflowJobDriver) terminate(ctx context.Context, job *client.WorkflowJob, status string, exitCode int32, msg string) {
	req := client.WorkflowJobTerminateRequest{
		Status:   status,
		ExitCode: exitCode,
		Message:  msg,
	}
	if err := d.Client.TerminateWorkflowJob(ctx, job.JobRunID, req); err != nil {
		log.Printf("workflow job %d: terminate: %v", job.JobRunID, err)
	}
}

// fail reports a pre-execution failure and returns the wrapped error.
func (d *WorkflowJobDriver) fail(ctx context.Context, job *client.WorkflowJob, e error) error {
	d.terminate(ctx, job, "failed", -1, e.Error())
	return e
}

// cloneWorkflowRepo clones the host repo and checks out a specific commit.
// Unlike cloneRepo (which is designed for agent sessions with working
// branches), this does a full clone with --no-checkout, then checks out
// the specific commit. Workflow jobs never push, but a private host repo
// still requires read auth to clone — so when the run carries a workflow
// token we wire the same kind of per-host inline credential helper the
// session clone uses, reading HANGRIX_WORKFLOW_TOKEN. A public repo (no
// token, or token rejected) clones anonymously as before.
func (d *WorkflowJobDriver) cloneWorkflowRepo(ctx context.Context, spec cloneSpec, commitSHA string) error {
	if err := os.RemoveAll(spec.Dest); err != nil {
		return fmt.Errorf("clear dest %s: %w", spec.Dest, err)
	}
	if err := os.MkdirAll(filepath.Dir(spec.Dest), 0o755); err != nil {
		return fmt.Errorf("ensure parent of %s: %w", spec.Dest, err)
	}

	gitURL := strings.TrimRight(spec.BaseURL, "/") + "/git/" + spec.Owner + "/" + spec.Name + ".git"

	// Full clone with --no-checkout first, then checkout the specific
	// commit. Using --no-checkout avoids issues with --branch not
	// accepting a raw SHA in older git.
	cloneArgs := []string{
		"clone",
		"--no-checkout",
	}
	var cloneEnv []string
	if spec.WorkflowToken != "" {
		cloneArgs = append(cloneArgs, "--config", spec.workflowCredentialHelperConfigArg())
		cloneEnv = []string{"HANGRIX_WORKFLOW_TOKEN=" + spec.WorkflowToken}
	}
	cloneArgs = append(cloneArgs, "--", gitURL, spec.Dest)
	if err := runGitWithEnv(ctx, "", cloneEnv, cloneArgs...); err != nil {
		return fmt.Errorf("clone %s: %w", gitURL, err)
	}

	checkoutArgs := []string{"checkout", commitSHA}
	if err := runGit(ctx, spec.Dest, checkoutArgs...); err != nil {
		return fmt.Errorf("checkout %s: %w", commitSHA, err)
	}
	return nil
}

// orchestratorVolumes converts client.Volume slices to orchestrator.Volume slices.
// When repoID > 0, each volume Name is prefixed as "repo-{repoID}-{name}"
// so Docker volumes are namespaced per repository.
func orchestratorVolumes(vols []client.Volume, repoID int64) []orchestrator.Volume {
	if len(vols) == 0 {
		return nil
	}
	out := make([]orchestrator.Volume, len(vols))
	for i, v := range vols {
		name := v.Name
		if repoID > 0 {
			name = fmt.Sprintf("repo-%d-%s", repoID, v.Name)
		}
		out[i] = orchestrator.Volume{Name: name, Mount: v.Mount}
	}
	return out
}

// ---- step output capture ----

// stepOutputPath returns the container-side path for the step output file.
// Uses the step's explicit ID when present, otherwise falls back to a
// 1-based index (e.g. "/tmp/hangrix/step-output-1").
func stepOutputPath(step client.WorkflowStep, stepIndex int) string {
	id := step.ID
	if id == "" {
		id = fmt.Sprintf("%d", stepIndex+1)
	}
	return fmt.Sprintf("/tmp/hangrix/step-output-%s", id)
}

// ensureStepOutputDir creates /tmp/hangrix inside the container so step
// output files can be written. Non-fatal: the job proceeds even if this
// fails (steps can still run, but output capture will fail).
func (d *WorkflowJobDriver) ensureStepOutputDir(ctx context.Context, containerID string) error {
	handle, err := d.Orchestrator.Exec(ctx, containerID, "/", nil, "mkdir", "-p", "/tmp/hangrix")
	if err != nil {
		return err
	}
	go io.Copy(io.Discard, handle.Stderr())
	io.Copy(io.Discard, handle.Stdout())
	_, err = handle.Wait()
	return err
}

// captureAndReportStepOutputs reads the step output file from inside
// the container, parses key=value lines, masks any values that match
// repo secrets, and reports the result to the platform. Called only
// for successful steps (exit code 0).
// Returns the parsed outputs map (nil when no outputs were captured)
// so the caller can accumulate them for subsequent step interpolation.
func (d *WorkflowJobDriver) captureAndReportStepOutputs(ctx context.Context, job *client.WorkflowJob, containerID string, step client.WorkflowStep, stepIndex int) map[string]string {
	outputPath := stepOutputPath(step, stepIndex)

	raw, err := d.readOutputFile(ctx, containerID, outputPath)
	if err != nil {
		log.Printf("workflow job %d: read step output %s: %v", job.JobRunID, outputPath, err)
	}

	outputs := parseOutputLines(raw)
	masked := maskSecretValues(outputs, job.RepoVariables)

	d.reportStepResult(ctx, job, step, stepIndex, 0, outputs, masked)

	// Clean up the output file so stale data doesn't leak to later steps.
	d.cleanupOutputFile(ctx, containerID, outputPath)

	return outputs
}

// reportStepResult sends a step result (exit code + optional outputs)
// to the platform. Idempotent — call even when outputs are nil.
func (d *WorkflowJobDriver) reportStepResult(ctx context.Context, job *client.WorkflowJob, step client.WorkflowStep, stepIndex int, exitCode int32, outputs map[string]string, masked []string) {
	// Normalize unnamed steps to their 1-based index so the server
	// always receives a non-empty step_id.
	stepID := step.ID
	if stepID == "" {
		stepID = fmt.Sprintf("%d", stepIndex+1)
	}
	req := client.WorkflowStepResultRequest{
		StepIndex: stepIndex,
		StepID:    stepID,
		ExitCode:  exitCode,
		Outputs:   outputs,
		Masked:    masked,
	}
	if err := d.Client.ReportWorkflowStepResult(ctx, job.JobRunID, req); err != nil {
		log.Printf("workflow job %d: report step result: %v", job.JobRunID, err)
	}
}

// readOutputFile execs `cat <path>` inside the container and returns
// the file contents. An error or non-zero exit means no outputs are
// available (file doesn't exist or couldn't be read).
func (d *WorkflowJobDriver) readOutputFile(ctx context.Context, containerID, path string) (string, error) {
	handle, err := d.Orchestrator.Exec(ctx, containerID, "/", nil, "cat", path)
	if err != nil {
		return "", fmt.Errorf("exec cat %s: %w", path, err)
	}

	// Drain stderr in background so stdout doesn't block.
	go io.Copy(io.Discard, handle.Stderr())

	data, err := io.ReadAll(handle.Stdout())
	if err != nil {
		return "", fmt.Errorf("read stdout: %w", err)
	}

	exitCode, err := handle.Wait()
	if err != nil {
		return "", fmt.Errorf("wait: %w", err)
	}
	if exitCode != 0 {
		return "", nil // file doesn't exist — no outputs
	}
	return string(data), nil
}

// cleanupOutputFile removes the step output file from the container.
// Errors are silently ignored — the file will be overwritten by the
// next step that shares the same path.
func (d *WorkflowJobDriver) cleanupOutputFile(ctx context.Context, containerID, path string) {
	handle, err := d.Orchestrator.Exec(ctx, containerID, "/", nil, "rm", "-f", path)
	if err != nil {
		return
	}
	go io.Copy(io.Discard, handle.Stderr())
	io.Copy(io.Discard, handle.Stdout())
	handle.Wait()
}

// parseOutputLines parses a raw output file into a map of key→value.
// Each non-empty line is split on the first '='; lines without '=' are
// skipped. Returns nil when no valid key=value pairs are found.
func parseOutputLines(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	outputs := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if key == "" {
			continue
		}
		outputs[key] = val
	}
	if len(outputs) == 0 {
		return nil
	}
	return outputs
}

// ---- step output interpolation ----

// stepOutputRefRe matches ${{ steps.<id>.outputs.<key> }} expressions.
// Group 1: step id (must be [a-z][a-z0-9-]* per spec).
// Group 2: output key (must be [a-zA-Z_][a-zA-Z0-9_-]* per spec).
var stepOutputRefRe = regexp.MustCompile(`\$\{\{\s*steps\.([a-z][a-z0-9-]*)\.outputs\.([a-zA-Z_][a-zA-Z0-9_-]*)\s*\}\}`)

// expandStepOutputRefs finds all ${{ steps.<id>.outputs.<key> }} references
// in text and replaces them with values captured from previous steps.
// Returns an error when a referenced step id or output key is not found —
// the caller should fail the job rather than silently injecting an empty
// string.
func expandStepOutputRefs(text string, stepOutputs map[string]map[string]string) (string, error) {
	if len(stepOutputs) == 0 {
		return text, nil
	}

	var err error
	result := stepOutputRefRe.ReplaceAllStringFunc(text, func(match string) string {
		subs := stepOutputRefRe.FindStringSubmatch(match)
		if len(subs) != 3 {
			err = fmt.Errorf("invalid step output reference: %s", match)
			return match
		}
		stepID := subs[1]
		key := subs[2]

		outputs, ok := stepOutputs[stepID]
		if !ok {
			err = fmt.Errorf("step %q not found (referenced in ${{ steps.%s.outputs.%s }})", stepID, stepID, key)
			return match
		}
		val, ok := outputs[key]
		if !ok {
			err = fmt.Errorf("output key %q not found in step %q (referenced in ${{ steps.%s.outputs.%s }})", key, stepID, stepID, key)
			return match
		}
		return val
	})
	if err != nil {
		return "", err
	}
	return result, nil
}

// variable/secret value. The runner treats all RepoVariables values as
// potentially sensitive — matching output values are flagged so the
// server can display them as "***".
// ---- release step execution ----

// releaseParamsFromWith decodes a release step's `with:` map into typed
// params. It is lenient: missing/ill-typed entries yield zero values, and
// `assets` accepts either a string path or a {path, name} object per entry.
// draft is a pointer so the caller can distinguish "omitted" (default true)
// from an explicit false.
func releaseParamsFromWith(with map[string]any) (tag, notes string, draft *bool, assets []client.WorkflowStepAsset) {
	tag = withString(with, "tag")
	notes = withString(with, "notes")
	if d, ok := with["draft"].(bool); ok {
		draft = &d
	}
	if raw, ok := with["assets"].([]any); ok {
		for _, e := range raw {
			switch v := e.(type) {
			case string:
				assets = append(assets, client.WorkflowStepAsset{Path: v})
			case map[string]any:
				assets = append(assets, client.WorkflowStepAsset{Path: withString(v, "path"), Name: withString(v, "name")})
			}
		}
	}
	return
}

// withString returns the string value at key k, or "" for missing /
// non-string entries.
func withString(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// runReleaseStep executes a "release" step by calling the platform release
// API. It does NOT exec into the container — release steps are runner-native.
//
// On success it returns the fixed step outputs (release_id, tag, draft,
// published, release_url) so the caller can index them for subsequent step
// interpolation.
func (d *WorkflowJobDriver) runReleaseStep(
	ctx context.Context,
	job *client.WorkflowJob,
	step client.WorkflowStep,
	stepIndex int,
	stepOutputs map[string]map[string]string,
	repoCheckout string,
) (map[string]string, error) {
	// 1. Decode release params from `with` and validate required fields.
	tagRaw, notes, draftPtr, assets := releaseParamsFromWith(step.With)
	tag := strings.TrimSpace(tagRaw)
	if tag == "" {
		return nil, fmt.Errorf("release step: with.tag is required")
	}

	// 2. Expand ${{ steps.<id>.outputs.<key> }} references in tag, notes,
	//    and asset paths/names.
	expandedTag, err := expandStepOutputRefs(tag, stepOutputs)
	if err != nil {
		return nil, fmt.Errorf("expand tag: %w", err)
	}
	expandedNotes, err := expandStepOutputRefs(notes, stepOutputs)
	if err != nil {
		return nil, fmt.Errorf("expand notes: %w", err)
	}

	expandedAssets := make([]client.WorkflowStepAsset, len(assets))
	for j, a := range assets {
		expPath, err := expandStepOutputRefs(a.Path, stepOutputs)
		if err != nil {
			return nil, fmt.Errorf("expand assets[%d].path: %w", j, err)
		}
		expName, err := expandStepOutputRefs(a.Name, stepOutputs)
		if err != nil {
			return nil, fmt.Errorf("expand assets[%d].name: %w", j, err)
		}
		expandedAssets[j] = client.WorkflowStepAsset{Path: expPath, Name: expName}
	}

	// 3. Resolve asset file paths relative to the repo checkout and
	//    validate each file exists and is readable.
	for j, a := range expandedAssets {
		resolved, err := d.resolveAssetPath(a.Path, repoCheckout)
		if err != nil {
			return nil, fmt.Errorf("asset %q: %w", a.Path, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("asset %q: %w", a.Path, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("asset %q: is a directory", a.Path)
		}
		// Store resolved path back so we open the right file later.
		expandedAssets[j].Path = resolved
	}

	// 4. Decide draft flag: default true.
	draft := true
	if draftPtr != nil {
		draft = *draftPtr
	}

	// 5. Create the release via platform API.
	d.appendSystemLog(ctx, job.JobRunID, fmt.Sprintf("creating release %q", expandedTag))

	rel, err := d.Client.CreateRelease(ctx, d.BaseURL, job.Owner, job.Name, job.WorkflowToken,
		client.CreateReleaseRequest{TagName: expandedTag, Notes: expandedNotes},
	)
	if err != nil {
		return nil, fmt.Errorf("create release: %w", err)
	}

	// 6. Upload assets.
	for _, a := range expandedAssets {
		assetName := a.Name
		if assetName == "" {
			assetName = filepath.Base(a.Path)
		}
		d.appendSystemLog(ctx, job.JobRunID, fmt.Sprintf("uploading asset %q from %s", assetName, a.Path))

		f, err := os.Open(a.Path)
		if err != nil {
			return nil, fmt.Errorf("open asset %q: %w", a.Path, err)
		}
		err = d.Client.UploadReleaseAsset(ctx, d.BaseURL, job.Owner, job.Name,
			job.WorkflowToken, rel.ID, assetName, "", f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("upload asset %q: %w", assetName, err)
		}
	}

	// 7. Publish if not a draft.
	published := false
	if !draft {
		d.appendSystemLog(ctx, job.JobRunID, fmt.Sprintf("publishing release %d", rel.ID))
		pubRel, err := d.Client.PublishRelease(ctx, d.BaseURL, job.Owner, job.Name,
			job.WorkflowToken, rel.ID)
		if err != nil {
			return nil, fmt.Errorf("publish release: %w", err)
		}
		published = true
		rel = pubRel
	}

	// 8. Build fixed step outputs.
	releaseURL := fmt.Sprintf("%s/%s/%s/releases/%d",
		strings.TrimRight(d.BaseURL, "/"), job.Owner, job.Name, rel.ID)

	outputs := map[string]string{
		"release_id":  strconv.FormatInt(rel.ID, 10),
		"tag":         rel.TagName,
		"draft":       strconv.FormatBool(rel.IsDraft),
		"published":   strconv.FormatBool(published),
		"release_url": releaseURL,
	}

	// Mask any output values that match repo secrets.
	masked := maskSecretValues(outputs, job.RepoVariables)
	d.reportStepResult(ctx, job, step, stepIndex, 0, outputs, masked)

	return outputs, nil
}

// ---- script step execution ----

// runScriptStep executes a "script" step by running the user's JavaScript
// via Node.js inside the container, with the hangrix SDK injected by the
// bootstrap bridge.
//
// On success it returns the captured step outputs so the caller can index
// them for subsequent step interpolation. Step outputs are read from the
// $HANGRIX_STEP_OUTPUT_FILE written by the bootstrap's flushOutputs().
func (d *WorkflowJobDriver) runScriptStep(
	ctx context.Context,
	job *client.WorkflowJob,
	containerID string,
	workingDir string,
	step client.WorkflowStep,
	stepIndex int,
	stepOutputs map[string]map[string]string,
	repoCheckout string,
) (map[string]string, error) {
	// 1. Build the base runtime env for this step.
	stepEnv, err := d.buildWorkflowEnv(job)
	if err != nil {
		return nil, fmt.Errorf("build env: %w", err)
	}

	// 2. Per-step env overrides (already expanded).
	if len(step.Env) > 0 {
		se := make(map[string]string, len(step.Env))
		for k, v := range step.Env {
			se[k] = v
		}
		if err := expandEnv(se, job.RepoVariables); err != nil {
			return nil, fmt.Errorf("expand step env: %w", err)
		}
		for k, v := range se {
			stepEnv[k] = v
		}
	}

	// 3. Inject HANGRIX_STEP_OUTPUT_FILE so the bootstrap knows where to
	//    write key=value outputs.
	stepOutputFile := stepOutputPath(step, stepIndex)
	stepEnv["HANGRIX_STEP_OUTPUT_FILE"] = stepOutputFile

	// 4. Serialise accumulated step outputs as JSON so the bootstrap's
	//    getStepOutputs() can serve them.
	stepOutputsJSON := ""
	if len(stepOutputs) > 0 {
		b, err := json.Marshal(stepOutputs)
		if err != nil {
			return nil, fmt.Errorf("marshal step outputs: %w", err)
		}
		stepOutputsJSON = string(b)
	}

	// 5. Build the scriptbridge driver and step spec.
	sd := &scriptbridge.Driver{
		Exec: func(ctx context.Context, cid, wd string, env map[string]string, args ...string) (scriptbridge.ExecHandle, error) {
			return d.Orchestrator.Exec(ctx, cid, wd, env, args...)
		},
		HostWorkdir:      repoCheckout,
		ContainerWorkdir: workingDir,
	}
	spec := scriptbridge.Step{
		ID:     step.ID,
		Name:   step.Name,
		Script: step.Script,
		Env:    step.Env,
		Dir:    step.Dir,
	}

	// 6. Run the script via the bridge.
	result := sd.Run(ctx, containerID, spec, stepEnv, stepOutputsJSON)
	if result.ErrorKind != "" {
		msg := result.ErrorMessage
		if result.ErrorDetails != nil {
			if code, ok := result.ErrorDetails["code"]; ok {
				msg = fmt.Sprintf("%s (code=%v)", msg, code)
			}
		}
		return nil, fmt.Errorf("%s: %s", result.ErrorKind, msg)
	}

	// 7. Read captured step outputs from the output file.
	outputs := d.captureAndReportStepOutputs(ctx, job, containerID, step, stepIndex)

	return outputs, nil
}

// resolveAssetPath resolves an asset path (from the step YAML) to an
// absolute host-side path. Absolute paths starting with "/workspace" are
// mapped to repoCheckout; relative paths are resolved against
// repoCheckout. The repoCheckout is the host-side directory that is
// bind-mounted as /workspace inside the container.
//
// Returns an error when the path is absolute but outside /workspace
// (e.g. /etc/passwd). Assets MUST be within the repo checkout.
func (d *WorkflowJobDriver) resolveAssetPath(assetPath, repoCheckout string) (string, error) {
	if filepath.IsAbs(assetPath) {
		// Map /workspace/... → repoCheckout/...
		if after, ok := strings.CutPrefix(assetPath, "/workspace/"); ok {
			return filepath.Join(repoCheckout, after), nil
		}
		if after, ok := strings.CutPrefix(assetPath, "/workspace"); ok && after == "" {
			return repoCheckout, nil
		}
		// Absolute paths outside /workspace are not allowed: they would
		// let a workflow author exfiltrate arbitrary host files.
		return "", fmt.Errorf("asset path %q is outside /workspace", assetPath)
	}
	// Relative path: resolve against repoCheckout.
	return filepath.Join(repoCheckout, assetPath), nil
}

func maskSecretValues(outputs, secrets map[string]string) []string {
	if len(outputs) == 0 || len(secrets) == 0 {
		return nil
	}
	secretValues := make(map[string]bool, len(secrets))
	for _, v := range secrets {
		if v != "" {
			secretValues[v] = true
		}
	}
	var masked []string
	for k, v := range outputs {
		if secretValues[v] {
			masked = append(masked, k)
		}
	}
	return masked
}
