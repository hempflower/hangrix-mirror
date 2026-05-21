package loop

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
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
			BaseURL:      d.BaseURL,
			Owner:        job.Owner,
			Name:         job.Name,
			SessionToken: "", // workflow jobs don't use session tokens
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
		orchestratorVolumes(job.Container.Volumes),
	)
	if err != nil {
		return d.fail(ctx, job, fmt.Errorf("create container: %w", err))
	}
	defer func() {
		if err := d.Orchestrator.RemoveContainer(context.Background(), containerID); err != nil {
			log.Printf("workflow job %d: remove container %s: %v", job.JobRunID, containerID, err)
		}
	}()

	// 7. Execute steps sequentially.
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

	for i, step := range job.Steps {
		stepName := step.Name
		if stepName == "" {
			stepName = step.Run
		}
		log.Printf("workflow job %d: step %d/%d %q", job.JobRunID, i+1, len(job.Steps), stepName)

		exitCode, err := d.runStep(stepCtx, job, containerID, workingDir, step)
		if err != nil {
			// Infrastructure failure (exec failed, context cancelled, etc.)
			d.appendSystemLog(ctx, job.JobRunID, fmt.Sprintf("step %q: %v", stepName, err))
			d.terminate(ctx, job, "failed", -1, err.Error())
			return nil // step error already reported; not a driver error
		}
		if exitCode != 0 {
			msg := fmt.Sprintf("step %q exited with code %d", stepName, exitCode)
			d.appendSystemLog(ctx, job.JobRunID, msg)
			d.terminate(ctx, job, "failed", exitCode, msg)
			return nil // step failure already reported
		}
	}

	// 8. Success.
	d.terminate(ctx, job, "success", 0, "")
	return nil
}

// runStep executes a single workflow step inside the container via
// docker exec bash -lc <run>. It streams stdout/stderr line-by-line
// to the platform and returns the exit code.
func (d *WorkflowJobDriver) runStep(ctx context.Context, job *client.WorkflowJob, containerID, workingDir string, step client.WorkflowStep) (int32, error) {
	// Build the runtime env for this step.
	stepEnv, err := d.buildWorkflowEnv(job)
	if err != nil {
		return -1, fmt.Errorf("build env: %w", err)
	}

	handle, err := d.Orchestrator.Exec(ctx, containerID, workingDir, stepEnv, "bash", "-lc", step.Run)
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
	if job.EventCauseID != "" {
		env["HANGRIX_EVENT_CAUSE_ID"] = job.EventCauseID
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
// the specific commit. No credential helper is set up (workflow jobs don't
// push).
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
		"--", gitURL, spec.Dest,
	}
	if err := runGitWithEnv(ctx, "", nil, cloneArgs...); err != nil {
		return fmt.Errorf("clone %s: %w", gitURL, err)
	}

	checkoutArgs := []string{"checkout", commitSHA}
	if err := runGit(ctx, spec.Dest, checkoutArgs...); err != nil {
		return fmt.Errorf("checkout %s: %w", commitSHA, err)
	}
	return nil
}

// orchestratorVolumes converts client.Volume slices to orchestrator.Volume slices.
func orchestratorVolumes(vols []client.Volume) []orchestrator.Volume {
	if len(vols) == 0 {
		return nil
	}
	out := make([]orchestrator.Volume, len(vols))
	for i, v := range vols {
		out[i] = orchestrator.Volume{Name: v.Name, Mount: v.Mount}
	}
	return out
}
