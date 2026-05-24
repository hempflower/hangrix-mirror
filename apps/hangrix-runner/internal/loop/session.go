// Package loop drives one runner: heartbeats + task polling + per-session
// agent lifecycle. The split between this file and the per-session driver
// is deliberate — the outer Loop is "what does the runner do all day",
// the SessionDriver is "what happens to one container from claim to done".
package loop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
)

// Default idle timeouts. The agent emits an `idle` outbound frame after
// each event, carrying a hint about how many background bash tasks are
// still alive. The runner picks the shorter timeout when there are no
// background jobs (cheap to retire the container; a fresh one will spin
// up on the next event) and the longer one when there are (the
// container is babysitting work; retiring early would orphan it).
const (
	defaultIdleTimeout         = 60 * time.Second
	defaultIdleTimeoutWithJobs = 5 * time.Minute
)

// SessionDriver runs one claimed session end-to-end. With long-lived
// containers, "one session" means "container start → events processed
// while alive → idle timeout → control:shutdown → container exit":
//
//  1. Resolve host paths (workdir, addendum file).
//  2. Start container via orchestrator.
//  3. Fan out IO goroutines:
//     stdin shipper:  poll /inputs → write to container stdin
//     stdout drain:   read container stdout, forward to /messages,
//     intercept `idle` to drive retirement
//     stderr drain:   read container stderr → log frames
//     idle watcher:   on `idle` start retirement timer; on timer
//     fire, write control:shutdown to stdin so the
//     agent cleans up background tasks and exits.
//  4. Wait for container exit, mark terminal.
//
// One driver per session; goroutines fan out internally and join before
// Run returns.
type SessionDriver struct {
	Client       *client.Client
	Orchestrator orchestrator.Orchestrator

	// Host paths the orchestrator binds into the container.
	AgentBinaryPath string
	WorkspaceRoot   string

	// BaseURL is the platform's reachable-from-container base. The
	// runner injects it into HANGRIX_PLATFORM_BASE_URL so the agent
	// can derive `/api/llm/v1/responses` and `/api/agent/tools/<name>`
	// for itself.
	BaseURL string

	// IdleTimeout / IdleTimeoutWithJobs override the package defaults
	// for this driver instance. Zero means "use the package default".
	IdleTimeout         time.Duration
	IdleTimeoutWithJobs time.Duration
}

// Run starts the container for the given task and stays in the IO loop
// until the container exits. Returns the exit code + an optional error.
// Never panics: any internal error is logged and converted into a
// terminal 'failed' session via the client.
func (d *SessionDriver) Run(ctx context.Context, task *client.Task) (exitCode int32, err error) {
	if err := d.Client.MarkRunning(ctx, task.SessionID); err != nil {
		log.Printf("session %d: mark running: %v", task.SessionID, err)
	}

	hostWorkdir := filepath.Join(d.WorkspaceRoot, fmt.Sprintf("session-%d", task.SessionID))
	repoCheckout := filepath.Join(hostWorkdir, "repo")
	if err := os.MkdirAll(hostWorkdir, 0o755); err != nil {
		return -1, d.fail(ctx, task.SessionID, fmt.Errorf("mkdir workdir: %w", err))
	}

	// Clone the host repo into hostWorkdir/repo before launching the
	// container. The agent sees a real working tree at /workspace and
	// can `git push` straight back to the platform — cloneRepo bakes
	// a per-host credential.helper into the cloned .git/config; that
	// helper reads $HANGRIX_SESSION_TOKEN at request time, so the same
	// .git/config works whether the server reuses the existing token
	// (the common resume path since issue #92) or rotates it. Sessions
	// without owner/name in env (admin smoke path) skip the clone
	// and get an empty workdir like before.
	//
	// We only clone on the FIRST trigger of a session (task.ContainerID
	// empty). Subsequent triggers reuse the long-lived container — its
	// /workspace bind mount is pinned to the same host inode created by
	// that first clone, and removing+re-creating the dir would orphan
	// the mount inside the container (runc then rejects `docker exec
	// --workdir /workspace` with "current working directory is outside
	// of container mount namespace root"). Skipping the re-clone is
	// also the point of long-lived containers in the first place:
	// caches, build artefacts, and in-flight edits survive across
	// turns. The previous turn's checkout is still on disk at
	// repoCheckout, so mountPath stays pointed at it for the fallback
	// case where the orchestrator has to rebuild a vanished container.
	owner := task.Env["HANGRIX_HOST_OWNER"]
	name := task.Env["HANGRIX_HOST_NAME"]
	mountPath := hostWorkdir
	if owner != "" && name != "" && task.SessionToken != "" {
		if task.ContainerID == "" {
			dest, err := cloneRepo(ctx, cloneSpec{
				BaseURL:       d.BaseURL,
				Owner:         owner,
				Name:          name,
				WorkingBranch: task.WorkingBranch,
				BaseBranch:    task.BaseBranch,
				SessionToken:  task.SessionToken,
				Dest:          repoCheckout,
			})
			if err != nil {
				return -1, d.fail(ctx, task.SessionID, fmt.Errorf("clone host repo: %w", err))
			}
			mountPath = dest
		} else {
			// Resume: container is reused; .git/config with the inline
			// credential helper is already in place from the first clone.
			// buildAgentEnv injects task.SessionToken as HANGRIX_SESSION_TOKEN;
			// on resume the server reuses the same token, so the agent and
			// credential helper see a consistent identity.
			mountPath = repoCheckout
		}
	}

	hostAddendumPath := ""
	if task.HostAddendum != "" {
		path := filepath.Join(hostWorkdir, "host_addendum.md")
		if err := os.WriteFile(path, []byte(task.HostAddendum), 0o600); err != nil {
			return -1, d.fail(ctx, task.SessionID, fmt.Errorf("write addendum: %w", err))
		}
		hostAddendumPath = path
	}

	// Expand ${VAR_NAME} references in env before layering runner-side
	// HANGRIX_* vars. Missing variables fail the session explicitly so
	// configuration mistakes don't silently inject empty strings.
	if err := expandEnv(task.Env, task.RepoVariables); err != nil {
		return -1, d.fail(ctx, task.SessionID, fmt.Errorf("expand env: %w", err))
	}

	env := buildAgentEnv(task, d.BaseURL)

	var buildSpec *orchestrator.BuildSpec
	if task.AgentBuild != nil {
		buildSpec = &orchestrator.BuildSpec{
			Dockerfile: task.AgentBuild.Dockerfile,
			Context:    task.AgentBuild.Context,
			Args:       task.AgentBuild.Args,
		}
	}
	otask := orchestrator.Task{
		SessionID:        task.SessionID,
		Image:            task.AgentImage,
		Entrypoint:       task.AgentEntrypoint,
		Build:            buildSpec,
		AgentBinaryPath:  d.AgentBinaryPath,
		HostAddendumPath: hostAddendumPath,
		HostWorkdir:      mountPath,
		Env:              env,
		ContainerID:      task.ContainerID,
		Volumes:          mapVolumes(task.Volumes, task.HostRepoID),
	}
	handle, err := d.Orchestrator.Start(ctx, otask)
	if err != nil {
		return -1, d.fail(ctx, task.SessionID, fmt.Errorf("start container: %w", err))
	}
	cid := handle.ContainerID()
	if cid != "" {
		if err := d.Client.SetContainer(ctx, task.SessionID, cid); err != nil {
			log.Printf("session %d: set container id: %v", task.SessionID, err)
		}
	}

	// Both shipStdin and the idle watcher write to the container's
	// stdin pipe. Wrap it in a mutex-guarded writer so their writes
	// can't interleave bytes in the middle of a JSON line.
	stdin := &lockedWriter{w: handle.Stdin()}

	// Seed the agent's first frame. The agent loop's first inbound MUST
	// be `kind:history` (see hangrix-agent/internal/runtime/loop.go); the
	// runner owns delivery of that frame so the invariant holds across
	// every agent process boot — fresh container, docker-exec into a
	// reused container, and crash-and-respawn paths alike. The platform
	// no longer enqueues this frame onto /inputs; we fetch it here once
	// and write it before the shipStdin goroutine starts pulling event
	// frames off the queue.
	historyFrame, err := d.Client.FetchHistory(ctx, task.SessionID)
	if err != nil {
		_ = handle.Stdin().Close()
		_, _ = handle.Wait()
		return -1, d.fail(ctx, task.SessionID, fmt.Errorf("fetch history: %w", err))
	}
	if _, err := stdin.Write(append([]byte(historyFrame), '\n')); err != nil {
		_ = handle.Stdin().Close()
		_, _ = handle.Wait()
		return -1, d.fail(ctx, task.SessionID, fmt.Errorf("write history frame: %w", err))
	}

	// activitySig is non-blocking: senders use a select-default to drop
	// when the watcher isn't ready. It only needs to flag "we observed
	// activity since the last idle"; missing one signal just delays
	// retirement by an idle-timeout-and-a-bit, which is harmless.
	activitySig := make(chan struct{}, 1)
	// idleSig carries the agent's running-jobs hint so the watcher can
	// pick the right timeout band. Buffered to one because the agent
	// emits exactly one idle frame between events.
	idleSig := make(chan idleSignal, 4)

	ioCtx, cancelIO := context.WithCancel(ctx)
	defer cancelIO()

	// pollCtx is a child of ioCtx that the watcher can cancel
	// independently. When the idle timer fires we want to stop pulling
	// new events off /inputs (else we'd ship them into a container
	// that's already on its way down and lose them from the agent's
	// perspective). The platform's /inputs queue keeps them safe; a
	// fresh container will pick them up.
	pollCtx, stopPolling := context.WithCancel(ioCtx)

	// stderrTail captures the agent's final stderr lines so a failed
	// session row carries the cause even if the appendLog HTTP fan-out
	// races the container exit (see shipStderr docs).
	stderrTail := newStderrTail(stderrTailCap)

	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		d.shipStdin(pollCtx, task.SessionID, stdin, activitySig)
	}()
	// shipStdout / shipStderr use the parent ctx (not ioCtx) for their
	// HTTP append calls so the agent's last words — written to stdout/
	// stderr microseconds before os.Exit(1) — still make it to the
	// platform after handle.Wait returns and cancelIO fires. Lifecycle
	// is governed by pipe EOF, which arrives naturally on container
	// exit; ioCtx is only for the actively-blocking goroutines below.
	go func() { defer wg.Done(); d.shipStdout(ctx, task.SessionID, handle.Stdout(), idleSig, activitySig) }()
	go func() { defer wg.Done(); d.shipStderr(ctx, task.SessionID, handle.Stderr(), stderrTail) }()
	go func() {
		defer wg.Done()
		d.watchIdle(ioCtx, task.SessionID, stdin, idleSig, activitySig, stopPolling)
	}()

	ec, waitErr := handle.Wait()
	cancelIO()
	wg.Wait()
	// Close the stdin pipe only after every writer goroutine has joined.
	// If shipStdin's defer closed it on poll-ctx cancel, the watcher's
	// own write (the shutdown frame that triggers the agent's exit
	// path) would race against an already-closed pipe and the test
	// would deadlock waiting on handle.Wait. Centralising the close
	// here makes the ordering invariant: writers stop first, then the
	// pipe is closed.
	_ = stdin.Close()

	exitCode = int32(ec)
	// Status mapping (unchanged from the one-shot era):
	//   * clean exit (ec=0, no waitErr) → idle. The container processed
	//     events and exited cleanly; the session row stays reusable so
	//     the next trigger rewakes it without losing identity.
	//   * non-zero exit OR waitErr → failed. The user / spawner can
	//     resume from the UI.
	status := client.TerminateRequest{Status: "idle", ExitCode: &exitCode}
	if waitErr != nil || ec != 0 {
		status.Status = "failed"
		switch {
		case waitErr != nil:
			status.Message = waitErr.Error()
		default:
			// Surface the agent's tail-of-stderr directly on the session
			// row so an operator opening the UI sees the cause without
			// having to scan log frames. The appendLog path delivers
			// the same lines into the message timeline; this is the
			// belt-and-suspenders copy for the most common shape
			// ("hangrix-agent: <one-line reason>" then exit 1).
			status.Message = stderrTail.String()
		}
	}
	if err := d.Client.Terminate(ctx, task.SessionID, status); err != nil {
		log.Printf("session %d: terminate: %v", task.SessionID, err)
	}
	return exitCode, waitErr
}

// fail is the short-circuit path when we can't even start the container.
// Reports the failure to the platform and returns the wrapped error so
// the caller can keep its own logs aligned.
func (d *SessionDriver) fail(ctx context.Context, sessionID int64, e error) error {
	code := int32(-1)
	if err := d.Client.Terminate(ctx, sessionID, client.TerminateRequest{
		Status:   "failed",
		ExitCode: &code,
		Message:  e.Error(),
	}); err != nil {
		log.Printf("session %d: terminate-on-fail: %v", sessionID, err)
	}
	return e
}

// lockedWriter serialises writes to the container's stdin. Both
// shipStdin (forwarding events from /inputs) and the idle watcher
// (writing control:shutdown when the retirement timer fires) take this
// lock, so a shutdown frame can never be sliced into the middle of an
// event frame.
type lockedWriter struct {
	mu sync.Mutex
	w  io.WriteCloser
}

func (lw *lockedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.Write(p)
}

func (lw *lockedWriter) Close() error {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.Close()
}

// idleSignal carries the agent's "I'm idle" report. RunningJobs is the
// snapshot count of background bash tasks alive in the agent at the
// moment idle was emitted; the watcher uses it to pick which timeout
// band to apply.
type idleSignal struct {
	runningJobs int
}

// emptyPollBackoff is the floor on how often shipStdin re-polls /inputs
// when the previous response was empty. Production servers do long-
// polling (they hold the request open until a frame is ready or a
// server-side timeout elapses), but defensive backoff protects us from
// a misconfigured short-poll server starving the scheduler in a tight
// loop — and gives the test stubs a reasonable cadence too.
const emptyPollBackoff = 50 * time.Millisecond

// shipStdin polls the platform for inbound IPC frames and writes each one
// (terminated by '\n') to the container stdin. Exits on context cancel
// (which the idle watcher does once it has decided to retire the
// container) or a write error (container exit closes the pipe).
//
// On every successful frame ship, we kick the activity signal so the
// idle watcher knows the agent is busy again and cancels its retirement
// timer.
func (d *SessionDriver) shipStdin(ctx context.Context, sessionID int64, w io.Writer, activity chan<- struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		resp, err := d.Client.PollInputs(ctx, sessionID)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Printf("session %d: poll inputs: %v", sessionID, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if len(resp.Frames) == 0 {
			// No work in the queue. Back off briefly so we don't
			// burn CPU spinning against a server that responds
			// instantly with an empty list. The select makes the
			// sleep cancellable so a context cancel still exits
			// promptly.
			select {
			case <-ctx.Done():
				return
			case <-time.After(emptyPollBackoff):
			}
			continue
		}
		for _, frame := range resp.Frames {
			// Flag activity FIRST, then write. Doing it the other way
			// around opens a race: the Write blocks until the agent
			// reads, which itself can trigger a downstream idleSig
			// (agent processes the frame and emits its idle ack),
			// and that idleSig can reach the watcher before the
			// activity signal does. The watcher then starts a fresh
			// timer and the late-arriving activity cancels it,
			// leaving us unable to ever retire the container.
			//
			// Sending activity before Write means it's strictly
			// earlier in real time than any downstream idleSig the
			// Write could provoke, so the watcher's drainStaleActivity
			// (after starting the timer) always catches it.
			select {
			case activity <- struct{}{}:
			default:
			}
			if _, err := w.Write(append([]byte(frame), '\n')); err != nil {
				return
			}
		}
	}
}

// shipStdout reads JSON-Lines off the container's stdout and forwards
// each frame to the platform as an /messages append, with two
// exceptions:
//   - `idle` frames are intercepted and routed to the watcher rather
//     than persisted; they're a runner-only control signal, not part
//     of the issue timeline.
//   - All other non-idle frames also trigger an activity signal, so
//     the watcher can see that the agent is mid-event (writing
//     messages, tool calls, log lines) and shouldn't be considered
//     truly idle even if it hasn't gotten back to the idle frame yet.
//
// ctx is the long-lived parent ctx (process scope), NOT ioCtx. That
// matters because the agent's final log frame — e.g. `{"kind":"log",
// "level":"error","msg":"llm call failed: ..."}` — is written just
// before os.Exit(1). The scanner drains it after handle.Wait returns
// and cancelIO has fired; if we forwarded it under ioCtx the
// AppendMessage HTTP call would see context.Canceled and the cause
// would silently vanish. The drain exits naturally on pipe EOF, so we
// don't need ioCtx for lifecycle here.
func (d *SessionDriver) shipStdout(
	ctx context.Context,
	sessionID int64,
	r io.Reader,
	idleSig chan<- idleSignal,
	activity chan<- struct{},
) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var frame outboundFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			d.appendLog(ctx, sessionID, "warn", "agent emitted non-JSON stdout: "+string(line))
			continue
		}
		if frame.Kind == "idle" {
			// Non-blocking: idleSig is a fast-path hint for watchIdle,
			// not a strict event stream. The buffer (size 4) absorbs
			// normal traffic — one idle per event — and dropping on
			// overflow is safe because watchIdle only consumes the
			// most recent signal to (re)arm its timer. Going
			// non-blocking also removes the need to watch ctx here: a
			// dead watcher (after cancelIO) no longer wedges shipStdout
			// against a full channel during the post-exit drain.
			select {
			case idleSig <- idleSignal{runningJobs: frame.RunningJobs}:
			default:
			}
			continue
		}
		// Non-idle frame = agent is doing something. Kick activity so
		// any pending retirement timer is cancelled.
		select {
		case activity <- struct{}{}:
		default:
		}
		// Bump container_last_used_at on every non-idle frame so
		// roster_list callers can see the most recent activity timestamp.
		if err := d.Client.Ping(ctx, sessionID); err != nil {
			log.Printf("session %d: bump activity: %v", sessionID, err)
		}
		req := frame.toAppendRequest()
		if err := d.Client.AppendMessage(ctx, sessionID, req); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Printf("session %d: append message: %v", sessionID, err)
		}
	}
}

// shipStderr forwards stderr lines as log frames AND mirrors them into
// a bounded in-memory tail buffer the caller reads on session
// termination. The agent's recover-on-panic line and the "hangrix-agent:
// <err>" written immediately before os.Exit(1) both land on stderr;
// keeping a copy on the runner side means the cause makes it onto the
// session's terminate.Message even if the appendLog HTTP call is
// racing the container exit. ctx here is the parent ctx (see shipStdout
// for the same rationale) so the appendLog tail post-handle.Wait
// survives cancelIO.
func (d *SessionDriver) shipStderr(ctx context.Context, sessionID int64, r io.Reader, tail *stderrTail) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 32*1024), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if tail != nil {
			tail.Add(line)
		}
		d.appendLog(ctx, sessionID, "info", "stderr: "+line)
	}
}

// watchIdle is the small state machine that decides when to retire the
// container. Inputs:
//   - idleSig: agent emitted `idle` (with a hint about background jobs)
//   - activity: a new event was shipped to stdin, OR the agent emitted
//     a non-idle outbound frame
//
// State: a single time.Timer that fires after the appropriate
// idle-timeout once idle is observed and no activity intervenes. When
// the timer fires we (a) stop the stdin poller so no new event gets
// shipped into a container we're already retiring, then (b) write a
// `control:shutdown` frame to stdin. The agent runs its cleanup and
// exits; handle.Wait() returns in Run; the function returns.
func (d *SessionDriver) watchIdle(
	ctx context.Context,
	sessionID int64,
	stdin io.Writer,
	idleSig <-chan idleSignal,
	activity <-chan struct{},
	stopPolling context.CancelFunc,
) {
	idleTimeout := d.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = defaultIdleTimeout
	}
	idleWithJobs := d.IdleTimeoutWithJobs
	if idleWithJobs <= 0 {
		idleWithJobs = defaultIdleTimeoutWithJobs
	}

	var timer *time.Timer
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
		}
	}
	defer stopTimer()

	sendShutdown := func() {
		// Stop pulling fresh /inputs before the agent acks shutdown,
		// otherwise an event that landed in the platform queue between
		// "timer fired" and "agent exited" would be shipped into a
		// dying container and lost. Once the agent exits and a new
		// trigger fires, a fresh container picks it up.
		stopPolling()
		if _, err := stdin.Write([]byte(`{"kind":"control","op":"shutdown"}` + "\n")); err != nil {
			log.Printf("session %d: send shutdown: %v", sessionID, err)
		}
	}

	for {
		var timerCh <-chan time.Time
		if timer != nil {
			timerCh = timer.C
		}
		select {
		case <-ctx.Done():
			return
		case sig := <-idleSig:
			// Agent reports idle. Pick the right timeout based on the
			// running-jobs hint. running_jobs>0 means a `bash`
			// background task is still alive — be generous, the agent
			// is conceptually waiting on it. running_jobs==0 means we
			// can retire whenever.
			d := idleTimeout
			if sig.runningJobs > 0 {
				d = idleWithJobs
			}
			stopTimer()
			timer = time.NewTimer(d)
			// Drain any stale activity signals queued before this
			// idle. Without this, an activity event that flowed from
			// shipStdin (shipping the very event the agent has now
			// finished processing) can race with the idle frame and
			// cancel the timer we just started — leaving us stuck
			// waiting for a retirement signal that never fires. The
			// idle frame is itself the agent's ack that those prior
			// events are done, so anything in activity at this moment
			// is by definition stale.
		drainStaleActivity:
			for {
				select {
				case <-activity:
				default:
					break drainStaleActivity
				}
			}
		case <-activity:
			// Agent is no longer idle: events being shipped or
			// messages flowing out. Cancel any pending retirement.
			stopTimer()
		case <-timerCh:
			sendShutdown()
			// After sending shutdown we wait for the container to exit
			// (handle.Wait in Run). Don't restart the timer; if the
			// agent races and emits another idle frame, we'd send a
			// second shutdown which is harmless but noisy. Just sit
			// here draining signals until ctx is cancelled.
			timer = nil
			for {
				select {
				case <-ctx.Done():
					return
				case <-idleSig:
				case <-activity:
				}
			}
		}
	}
}

func (d *SessionDriver) appendLog(ctx context.Context, sessionID int64, level, msg string) {
	_ = d.Client.AppendMessage(ctx, sessionID, client.AppendMessageRequest{
		Kind:  "log",
		Level: level,
		Msg:   msg,
	})
}

// outboundFrame mirrors apps/hangrix-agent/internal/ipc.Outbound on the
// wire. We deliberately keep our own copy here (instead of importing the
// agent package) so the runner binary doesn't transitively pull the
// agent's third-party deps.
type outboundFrame struct {
	Kind        string          `json:"kind"`
	Phase       string          `json:"phase,omitempty"`
	Role        string          `json:"role,omitempty"`
	Content     string          `json:"content,omitempty"`
	ToolCalls   []toolCall      `json:"tool_calls,omitempty"`
	Name        string          `json:"name,omitempty"`
	Args        json.RawMessage `json:"args,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	ToolCallID  string          `json:"tool_call_id,omitempty"`
	Level       string          `json:"level,omitempty"`
	Msg         string          `json:"msg,omitempty"`
	TurnID      string          `json:"turn_id,omitempty"`
	RunningJobs int             `json:"running_jobs,omitempty"`
}

type toolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (f outboundFrame) toAppendRequest() client.AppendMessageRequest {
	req := client.AppendMessageRequest{
		Kind:       f.Kind,
		Role:       f.Role,
		Content:    f.Content,
		Phase:      f.Phase,
		Level:      f.Level,
		Msg:        f.Msg,
		Name:       f.Name,
		Args:       f.Args,
		Result:     f.Result,
		ToolCallID: f.ToolCallID,
		TurnID:     f.TurnID,
	}
	if len(f.ToolCalls) > 0 {
		req.ToolCalls = make([]client.ToolCallDTO, len(f.ToolCalls))
		for i, c := range f.ToolCalls {
			req.ToolCalls[i] = client.ToolCallDTO{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
		}
	}
	return req
}

// expandEnv expands ${VAR_NAME} whole-value references in env using the
// repo-level variable map (task.RepoVariables). Only whole-value
// references (e.g. FOO: ${BAR}) are expanded; partial references like
// FOO: prefix-${BAR} and non-references pass through unchanged. The
// variable name between ${ and } must match [A-Za-z_][A-Za-z0-9_]*.
//
// Missing variables return an error naming the first missing name and
// the env key that referenced it. The caller MUST fail the session on
// error — silently injecting an empty string would mask configuration
// mistakes.
func expandEnv(env map[string]string, repoVars map[string]string) error {
	if repoVars == nil {
		return nil // server hasn't been updated yet — backward compat
	}
	for k, v := range env {
		if len(v) < 4 || v[0] != '$' || v[1] != '{' || v[len(v)-1] != '}' {
			continue
		}
		varName := v[2 : len(v)-1]
		if !isEnvVarName(varName) {
			continue
		}
		replacement, ok := repoVars[varName]
		if !ok {
			return fmt.Errorf("env %q references undefined variable %q", k, varName)
		}
		env[k] = replacement
	}
	return nil
}

// isEnvVarName reports whether s is a valid variable name for ${...}
// expansion: non-empty, starts with a letter or underscore, and contains
// only letters, digits, and underscores.
func isEnvVarName(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i, c := range s {
		if i == 0 {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_') {
				return false
			}
		} else {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
				return false
			}
		}
	}
	return true
}

// buildAgentEnv assembles the HANGRIX_* env vars the agent expects. We
// start from whatever the platform sent (its ExtraEnv plus any role
// hints), then layer the runner-side override on top so the in-container
// agent knows where the platform lives. The agent derives the LLM
// `/api/llm/v1/responses` and tool `/api/agent/tools/<name>` paths
// from the same base — see apps/hangrix-agent/internal/config/config.go.
func buildAgentEnv(task *client.Task, baseURL string) map[string]string {
	env := map[string]string{}
	for k, v := range task.Env {
		env[k] = v
	}
	if task.SessionToken != "" {
		env["HANGRIX_SESSION_TOKEN"] = task.SessionToken
	}
	if baseURL != "" {
		env["HANGRIX_PLATFORM_BASE_URL"] = baseURL
	}
	env["HANGRIX_SESSION_ID"] = strconv.FormatInt(task.SessionID, 10)
	if task.Role != "" {
		env["HANGRIX_ROLE"] = task.Role
	}
	if task.Model != "" {
		env["HANGRIX_LLM_MODEL"] = task.Model
	}
	if task.IssueNumber > 0 {
		env["HANGRIX_ISSUE_NUMBER"] = strconv.FormatInt(int64(task.IssueNumber), 10)
	}
	if task.WorkingBranch != "" {
		env["HANGRIX_WORKING_BRANCH"] = task.WorkingBranch
	}
	if task.BaseBranch != "" {
		env["HANGRIX_BASE_BRANCH"] = task.BaseBranch
	}
	if len(task.McpServers) > 0 {
		env["HANGRIX_MCP_SERVERS"] = strings.Join(task.McpServers, ",")
	}
	return env
}

// mapVolumes converts client.Volume slices to orchestrator.Volume slices.
// When repoID > 0, each volume Name is prefixed as "repo-{repoID}-{name}"
// so Docker volumes are namespaced per repository. repoID == 0 means the
// server hasn't been upgraded yet — names pass through verbatim for
// backward compatibility.
func mapVolumes(vols []client.Volume, repoID int64) []orchestrator.Volume {
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

// stderrTailCap bounds how much of the agent's stderr we keep around
// to attach to a failed session's terminate.Message. Sized to fit a
// handful of typical hangrix-agent error lines plus any pre-recover
// stderr noise; large enough to carry context, small enough that the
// /terminate payload never bloats.
const stderrTailCap = 4096

// stderrTail is a small bounded buffer that retains the *last* N bytes
// of the agent's stderr. shipStderr appends each line; Run reads the
// snapshot on session termination to surface the cause via
// terminate.Message. Concurrency: shipStderr (one goroutine) appends,
// Run reads after wg.Wait, so a single mutex covers it without contention.
type stderrTail struct {
	mu    sync.Mutex
	lines []string
	bytes int
	cap   int
}

func newStderrTail(cap int) *stderrTail {
	return &stderrTail{cap: cap}
}

func (t *stderrTail) Add(line string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	t.bytes += len(line) + 1 // +1 for the join newline
	// Trim from the front until we fit under cap. Keep at least one
	// line so a single very long line still produces a (truncated)
	// non-empty tail.
	for t.bytes > t.cap && len(t.lines) > 1 {
		t.bytes -= len(t.lines[0]) + 1
		t.lines = t.lines[1:]
	}
}

func (t *stderrTail) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := strings.Join(t.lines, "\n")
	// Final guard: if a single retained line still exceeds cap, clip
	// it so terminate.Message never balloons past the bound.
	if len(s) > t.cap {
		s = s[len(s)-t.cap:]
	}
	return s
}
