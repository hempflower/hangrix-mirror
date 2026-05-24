package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// bash runs commands in `bash -c` under a PTY. Allocating a PTY (rather
// than wiring stdout/stderr to plain pipes) buys us three things the LLM
// genuinely needs:
//
//  1. isatty() = true — programs that probe it (progress-bar tools,
//     CLIs that switch to line-buffered output, etc.) see a real
//     terminal and behave naturally instead of degrading to weird modes
//     ("running in a non-interactive shell").
//
//  2. Clean, predictable output — the agent default is TERM=dumb (see
//     agentEnvDefaults below), so programs that check terminfo see a
//     minimal terminal that advertises NO colour and NO cursor
//     addressing.  They still produce output (no hangs, no "terminal is
//     not fully functional" warnings for terminfo-aware programs), but
//     they won't emit ANSI escape sequences that pollute agent-visible
//     logs.  PAGER=cat and friends divert pagers before they can react
//     to the dumb terminal.
//
//     This is a deliberate narrowing of the PTY contract: we guarantee
//     isatty()=true, line-buffered output, and stdin for interactive
//     prompts (bash_input), but we do NOT promise full colour or
//     cursor-addressing capabilities by default.  Users who need richer
//     terminal features (e.g. TUI-heavy workloads) can export
//     TERM=xterm-256color in their runtime config — the parent
//     environment wins over agentEnvDefaults.
//
//  3. A real stdin we can write to mid-flight. The `bash_input` tool
//     uses that to answer interactive prompts (y/N confirmations,
//     password fields, etc.) on a background task.
//
// All output (merged at the kernel level by the PTY) is streamed into a
// per-job temp file. The synchronous (foreground) path waits for the
// command to finish and returns the file contents; the background path
// returns a task_id immediately and the same file keeps filling up,
// readable on every poll. Going through a file rather than an in-memory
// buffer is a small bet on the future — it's bounded by disk instead of
// RAM, and the file path is exposed back to the LLM so it can `tail -f`
// or grep without polling.
//
// Foreground calls **auto-promote** to background mode if they're still
// running after foregroundPromoteAfter (30s). The command keeps running,
// a task_id is handed back, and the LLM can either poll later or move
// on to other work. The original tool call returns within ~30s no
// matter how long the underlying command takes, so a slow `apt-get` or
// a hung subprocess never burns a whole turn waiting.

// foregroundPromoteAfter is the wall-clock threshold past which a
// synchronous bash call gets converted to a background task. Tests
// shorten it via SetForegroundPromoteAfterForTest to keep the suite from
// spinning for half a minute on each assertion.
var foregroundPromoteAfter = 30 * time.Second

// SetForegroundPromoteAfterForTest swaps the synchronous-wait budget
// the foreground bash path uses before auto-promoting to background,
// and returns a restore func. Tests-only; production code MUST NOT
// depend on flipping this at runtime.
func SetForegroundPromoteAfterForTest(d time.Duration) (restore func()) {
	prev := foregroundPromoteAfter
	foregroundPromoteAfter = d
	return func() { foregroundPromoteAfter = prev }
}

// agentEnvDefaults are the env vars we want every bash subprocess to see
// unless the parent process has already set them explicitly. The agent
// runs unattended, so anything that opens a pager, prompts for input, or
// pops a TUI dialog blocks forever — these defaults nudge well-behaved
// CLIs into their non-interactive code paths while leaving the PTY alive
// (programs that genuinely want isatty/colour still get it).
//
// Each entry is "KEY=VALUE". Order doesn't matter; lookups are by key.
var agentEnvDefaults = []string{
	// TERM=dumb tells every program the terminal has minimal capabilities
	// — no colour, no cursor movement. Combined with PAGER=cat (which
	// diverts pagers before they reach terminfo) and CI=true, this makes
	// CLI output plain and predictable. The PTY still makes isatty()
	// true so programs that gate line-buffering or progress spinners on
	// it still behave naturally, but they won't emit escape sequences
	// that pollute logs.
	//
	// Trade-off: "dumb" is a valid terminfo entry and ncurses queries
	// (tput, infocmp) complete instantly, but less(1) will display a
	// "terminal is not fully functional / Press RETURN" warning and hang
	// if invoked directly — even with LESS=-FRX. The mitigation is that
	// PAGER=cat, GIT_PAGER=cat, MANPAGER=cat, and SYSTEMD_PAGER=cat
	// (set below) prevent less from being invoked as a pager by git,
	// man, systemctl, and any tool that honours PAGER. Only an explicit
	// `less foo.txt` in a bash command would trigger the hang, and
	// agents use `read` / `grep` for file inspection, not pagers.
	// See TestBashTermDumbSafeForPTY and TestBashTermDumbInteractiveInput.
	"TERM=dumb",

	// Kill pagers across the ecosystem. `cat` is the universal "just dump
	// it" pager. LESS=-FRX is the belt-and-suspenders backup for anything
	// that bypasses PAGER and invokes less directly: -F exits if the
	// output fits one screen, -R passes ANSI through, -X skips the
	// init/deinit sequences that can leave the PTY in a weird state.
	"PAGER=cat",
	"GIT_PAGER=cat",
	"SYSTEMD_PAGER=cat",
	"MANPAGER=cat",
	"LESS=-FRX",

	// The de-facto "I'm in CI / unattended" signal. npm, yarn, pnpm,
	// cargo, gh, playwright, and many others switch to non-interactive
	// modes on CI=true (no progress bars, no prompts, fail-fast on
	// missing input).
	"CI=true",

	// apt/dpkg/debconf: never pop a TUI dialog. Without this an
	// `apt-get install` of anything touching configs (postfix, mysql,
	// tzdata) will sit forever on a purple ncurses screen.
	"DEBIAN_FRONTEND=noninteractive",

	// Real-time Python output instead of block-buffered, and skip the
	// pip prompts/version chatter that pollute logs.
	"PYTHONUNBUFFERED=1",
	"PIP_DISABLE_PIP_VERSION_CHECK=1",
	"PIP_NO_INPUT=1",

	// Quiet npm: no funding/audit banner, no progress bar churn.
	"NPM_CONFIG_FUND=false",
	"NPM_CONFIG_AUDIT=false",
	"NPM_CONFIG_PROGRESS=false",
}

// agentEnv returns the parent process environment with agentEnvDefaults
// layered on top *only for keys not already set*. Parent env wins, so a
// user who deliberately exports e.g. TERM=xterm-256color in their runtime config
// keeps that value.
func agentEnv() []string {
	parent := os.Environ()
	seen := make(map[string]struct{}, len(parent))
	for _, kv := range parent {
		if i := strings.IndexByte(kv, '='); i > 0 {
			seen[kv[:i]] = struct{}{}
		}
	}
	out := make([]string, 0, len(parent)+len(agentEnvDefaults))
	out = append(out, parent...)
	for _, kv := range agentEnvDefaults {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		if _, already := seen[kv[:i]]; already {
			continue
		}
		out = append(out, kv)
	}
	return out
}

type bashArgs struct {
	Command         string `json:"command"`
	// Summary is a short (one-line, ~7 words) human-readable description
	// of WHAT the command does — the LLM supplies it on every fresh call
	// so the agent-log UI has a "Running tests" / "Installing deps" chip
	// next to each bash tool call instead of squashing the literal
	// command into the row. It is echoed back unchanged in the result.
	// Not used for polls (task_id calls): the original summary is
	// carried on the job itself.
	Summary         string `json:"summary"`
	WorkingDir      string `json:"working_dir"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	RunInBackground bool   `json:"run_in_background"`
	TaskID          string `json:"task_id"` // poll an earlier background task
}

type bashResult struct {
	// Summary is a short one-line description of the command being run.
	// It exists for the UI: collapsed tool-call rows need a "what did this
	// do" chip alongside the tool name, and pulling it from `command`
	// client-side would mean every consumer reinvents the same first-line/
	// truncate logic. The LLM sees it as well, but it's redundant with the
	// args.command it already has.
	Summary    string `json:"summary,omitempty"`
	Output     string `json:"output"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	TaskID     string `json:"task_id,omitempty"`
	Status     string `json:"status,omitempty"`      // "running" | "done" | "promoted" | "" (sync result)
	OutputFile string `json:"output_file,omitempty"` // path to the per-job log; set on all results (enables unified size guard)
}

type bashJob struct {
	cmd       *exec.Cmd
	ptmx      *os.File  // master end of the PTY; nil if pty.Start failed
	outPath   string    // temp file the PTY reader streams into
	command   string    // verbatim shell command, snapshotted so the cleanup path can still describe it after the cmd object is gone
	summary   string    // LLM-supplied one-liner; echoed back on every result/poll so the UI keeps its chip across polls
	startedAt time.Time // wall-clock start of the job; used for "n seconds elapsed" notifications
	taskID    string    // back-pointer so a job can describe itself in a notification without the bashTool re-doing the map lookup
	mu        sync.Mutex
	done      bool
	exitCode  int
	timedOut  bool
	cancel    context.CancelFunc
	doneCh    chan struct{} // closed once the wait goroutine has set the final state
}

func (j *bashJob) snapshot() string {
	if j.outPath == "" {
		return ""
	}
	// Each poll re-reads the whole file. Cheap for the sizes we expect
	// (a few KB to a few MB); if it ever becomes a hot path we can layer
	// in an offset-tracking reader. The file is being appended to on
	// another goroutine — POSIX append semantics mean a concurrent read
	// just sees whatever has been flushed at this instant, which is the
	// behaviour we want.
	data, err := os.ReadFile(j.outPath)
	if err != nil {
		return ""
	}
	return string(data)
}

type sleepTimer struct {
	timer *time.Timer
	msg   string
}

type bashTool struct {
	mu   sync.Mutex
	jobs map[string]*bashJob

	// timers tracks pending sleep schedules. Guarded by timersMu (separate
	// from mu so the timer-fired callback — which runs on the time package's
	// goroutine — doesn't contend with the bash job map lock).
	timersMu sync.Mutex
	timers   map[string]*sleepTimer

	// notifyCh fans background-job completions out to whoever subscribed
	// via NotificationCh — in practice that's the agent runtime loop,
	// which drains pending notifications into the LLM context at the
	// start of each round. Buffered so the wait goroutine doesn't stall
	// when no one is reading yet (e.g. a job completes mid-LLM-call and
	// the loop is parked in llm.Create). The buffer is large enough to
	// hold every possible job's terminal record without back-pressure;
	// see Notify below.
	notifyCh chan string
}

func newBashTool() *bashTool {
	return &bashTool{
		jobs:     map[string]*bashJob{},
		timers:   map[string]*sleepTimer{},
		notifyCh: make(chan string, 64),
	}
}

// NotificationCh exposes the read end of the bashTool's notification
// stream. Each value on the channel is a single user-role text snippet
// the runtime loop should append to the LLM conversation at the next
// drain point (see runtime.Loop). The channel is closed only when the
// agent process is shutting down; callers MUST be ready for "no
// notifications, indefinitely" and select against it alongside the
// other inbox sources.
func (b *bashTool) NotificationCh() <-chan string { return b.notifyCh }

// HasRunningJobs reports how many background bash tasks have not yet
// reached `done` state. The runtime uses this to decide whether to
// continue waiting (e.g. before letting a control:shutdown unwind the
// process) and to populate the `running_jobs` hint on the `idle`
// outbound frame.
func (b *bashTool) HasRunningJobs() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, j := range b.jobs {
		j.mu.Lock()
		if !j.done {
			n++
		}
		j.mu.Unlock()
	}
	b.timersMu.Lock()
	n += len(b.timers)
	b.timersMu.Unlock()
	return n
}

// Cleanup gracefully takes down every still-running background job,
// waiting up to the supplied context's deadline (or grace if the
// context has none) for each one to acknowledge SIGKILL by closing its
// doneCh. Called on agent shutdown; bounded so an unkillable child
// can't wedge the exit path indefinitely.
//
// The caller is responsible for picking a reasonable timeout — 3-5
// seconds is typical. Past that, container teardown will reap whatever
// is left.
func (b *bashTool) Cleanup(ctx context.Context) {
	// Cancel every pending sleep timer first. No notification is sent
	// for cancelled timers — the session is shutting down.
	b.timersMu.Lock()
	for id, t := range b.timers {
		t.timer.Stop()
		delete(b.timers, id)
	}
	b.timersMu.Unlock()

	b.mu.Lock()
	running := make([]*bashJob, 0, len(b.jobs))
	for _, j := range b.jobs {
		j.mu.Lock()
		alive := !j.done
		j.mu.Unlock()
		if alive {
			running = append(running, j)
		}
	}
	b.mu.Unlock()

	if len(running) == 0 {
		return
	}
	// Cancel everyone first so the kills race in parallel; THEN wait.
	// Doing the wait inside the loop above would serialise the deadline
	// across N jobs.
	for _, j := range running {
		j.cancel()
	}
	for _, j := range running {
		select {
		case <-j.doneCh:
		case <-ctx.Done():
			return
		}
	}
}

// Schedule fires a notification after the given duration. Returns an
// opaque sleep ID. The notification text is sent to NotificationCh when
// the timer fires. The timer counts as a running job until it fires or
// is cancelled.
func (b *bashTool) Schedule(d time.Duration, notification string) string {
	id := newSleepID()
	b.ScheduleWithID(id, d, notification)
	return id
}

// ScheduleWithID is like Schedule but uses the caller-provided id
// instead of generating one. The caller is responsible for ensuring the
// id is unique (e.g. via newSleepID()).
func (b *bashTool) ScheduleWithID(id string, d time.Duration, notification string) {
	b.timersMu.Lock()
	b.timers[id] = &sleepTimer{
		timer: time.AfterFunc(d, func() {
			b.timersMu.Lock()
			delete(b.timers, id)
			b.timersMu.Unlock()
			b.notify(notification)
		}),
		msg: notification,
	}
	b.timersMu.Unlock()
}

// CancelSchedule cancels a previously scheduled notification. No
// notification is sent. Safe to call on an already-fired ID (no-op).
func (b *bashTool) CancelSchedule(id string) {
	b.timersMu.Lock()
	t, ok := b.timers[id]
	if ok {
		t.timer.Stop()
		delete(b.timers, id)
	}
	b.timersMu.Unlock()
}

// notify pushes a terminal notification onto the bashTool's
// notification channel. Non-blocking: when the buffer is full we drop
// rather than stall the wait goroutine — a missed notification is a
// far smaller failure than a hung agent. The buffer size (64) is
// generous enough that this only happens in pathological scenarios.
func (b *bashTool) notify(msg string) {
	select {
	case b.notifyCh <- msg:
	default:
	}
}

func (*bashTool) Name() string { return "bash" }
func (*bashTool) Description() string {
	return "Run a shell command via 'bash -c' under a PTY (bashisms like pipefail, process substitution, and [[ ]] are available). " +
		"Always pass a short 'summary' (5–7 words, imperative — e.g. 'Run unit tests', 'Install dependencies', 'List repo files'); the UI uses it as the collapsed-row label, so omit it and the agent log just shows a generic 'bash' chip. " +
		"Stdout and stderr are merged into the result's 'output' field, the same way they would be in an interactive terminal — pipe explicitly inside the command (e.g. 'cmd 2>/tmp/err') if you need them split. " +
		"Foreground calls auto-promote to background after 30 seconds: the original call returns with status='promoted' and a task_id while the command keeps running, so a long apt/build/test never blocks a whole turn. " +
		"Set run_in_background=true to skip the synchronous wait entirely; the response includes a tool-generated task_id and an output_file path the command's stream is written to. " +
		"To check progress, call bash again with that task_id and no command — task_id is opaque, do not invent or modify it, and do not supply it together with command in the same call. " +
		"To answer an interactive prompt on a background task (y/N confirmations, password fields, REPLs), use the bash_input tool with the same task_id."
}
func (*bashTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":           map[string]any{"type": "string", "description": "Shell command to execute. Required unless task_id is given."},
			"summary":           map[string]any{"type": "string", "description": "Short (5–7 word) human-readable description of what this command does. Imperative voice — 'Run unit tests', 'Install dependencies', 'Check disk usage'. Used as the collapsed-row label in the agent log UI. Required when starting a new command; ignored on task_id polls."},
			"working_dir":       map[string]any{"type": "string"},
			"timeout_seconds":   map[string]any{"type": "integer", "description": "Maximum lifetime of the command in seconds. Default 120. Synchronous calls auto-promote to background after 30 seconds even when this is larger."},
			"run_in_background": map[string]any{"type": "boolean", "description": "Start the command in the background immediately (skip the 30s synchronous wait). The response carries a tool-generated task_id you pass back to poll or feed stdin via bash_input."},
			"task_id":           map[string]any{"type": "string", "description": "Opaque id returned by an earlier run_in_background=true call or by a promoted foreground call. Pass it back (with no command) to poll progress. Mutually exclusive with command — never supply both, and never invent a value."},
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
	return b.runForeground(ctx, a), nil
}

// spawnJob is the shared engine behind both runForeground and
// spawnBackground. It starts the command under a PTY, opens a temp file
// for streamed output, and kicks off the two goroutines that drain the
// PTY and reap the process. Callers decide whether to wait for the job
// or to hand the LLM a task_id straight away.
func (b *bashTool) spawnJob(a bashArgs) *bashJob {
	cctx, cancel := context.WithCancel(context.Background())
	if a.TimeoutSeconds > 0 {
		cctx, cancel = context.WithTimeout(cctx, time.Duration(a.TimeoutSeconds)*time.Second)
	}
	cmd := exec.CommandContext(cctx, "bash", "-c", a.Command)
	if a.WorkingDir != "" {
		cmd.Dir = a.WorkingDir
	}
	// Inject agent-friendly defaults (no pagers via PAGER=cat,
	// no TUI dialogs, TERM=dumb for clean output, CI=true) on top
	// of the parent env.
	cmd.Env = agentEnv()
	// On context cancel/timeout, take down the whole session, not just
	// bash. pty.Start sets Setsid, so the child is its own session leader
	// and process-group head — `kill -PID` reaches every descendant that
	// hasn't detached.
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// WaitDelay bounds how long exec waits after Cancel before reaping,
	// and is the safety net that lets a successful `./srv &; exit 0`
	// return instead of blocking forever on a grandchild that still
	// holds the slave-side FD.
	cmd.WaitDelay = 2 * time.Second

	job := &bashJob{
		cmd:       cmd,
		command:   a.Command,
		summary:   a.Summary,
		startedAt: time.Now(),
		cancel:    cancel,
		doneCh:    make(chan struct{}),
	}

	outFile, err := os.CreateTemp("", "hangrix-bash-*.log")
	if err != nil {
		// File creation failures are exceedingly rare and indicate a
		// genuinely broken environment (no /tmp, no FDs). Surface the
		// error via the same -1/error-text shape as a start failure.
		job.exitCode = -1
		cancel()
		// Fabricate an in-memory-only path so snapshot() returns the
		// error text. Easier than threading a special-case through.
		ramPath := writeEphemeralError(fmt.Sprintf("bash: create temp output file: %v", err))
		job.outPath = ramPath
		job.done = true
		close(job.doneCh)
		return job
	}
	job.outPath = outFile.Name()

	ptmx, err := pty.Start(cmd)
	if err != nil {
		// Same uniform shape as the rest of the tool: -1 exit + the
		// error text in the output file so snapshot() returns something
		// the LLM can read.
		_, _ = outFile.WriteString("bash: " + err.Error())
		_ = outFile.Close()
		job.exitCode = -1
		job.done = true
		cancel()
		close(job.doneCh)
		return job
	}
	job.ptmx = ptmx

	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		// PTY reads return EIO once every fd referencing the slave is
		// closed; io.Copy translates that into a clean return.
		_, _ = io.Copy(outFile, ptmx)
	}()

	go func() {
		waitErr := cmd.Wait()
		// Sweep the group first: a successful `./srv &; exit 0` leaves
		// the grandchild alive and holding the slave FD, which would
		// otherwise keep io.Copy blocked forever. Sending to a finished
		// group is harmless (ESRCH, ignored).
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		// Let io.Copy drain the master naturally. PTY reads return EIO
		// once all FDs referencing the slave are closed AND the kernel
		// buffer is empty — closing the master *before* the drain
		// finishes loses whatever was still buffered (the common cause
		// of short commands like `echo foo` returning empty output).
		// Force-close only as a fallback for the grandchild-still-holds-
		// slave case after we've killed the group.
		select {
		case <-copyDone:
			// Drained naturally — close the master to release the FD.
			_ = ptmx.Close()
		case <-time.After(2 * time.Second):
			// Grandchild is still holding the slave. Force EOF on the
			// reader; the second close-from-the-error-branch is harmless.
			_ = ptmx.Close()
			<-copyDone
		}
		_ = outFile.Close()

		job.mu.Lock()
		if cctx.Err() == context.DeadlineExceeded {
			job.timedOut = true
		}
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				job.exitCode = exitErr.ExitCode()
			} else if !job.timedOut {
				job.exitCode = -1
			} else {
				job.exitCode = -1
			}
		}
		job.done = true
		taskID := job.taskID
		job.mu.Unlock()
		close(job.doneCh)

		// Notify the runtime if this job had been promoted/registered
		// as a background task — that's the case where the LLM
		// otherwise would have to poll to learn it finished. Foreground
		// jobs that completed in the synchronous budget don't carry a
		// taskID (the result was already returned inline) and don't
		// fan out here. registerTaskID handles the inverse race —
		// completion before registration — by checking done at attach
		// time and emitting immediately.
		if taskID != "" {
			b.notify(formatJobNotification(job))
		}
	}()

	return job
}

// runForeground starts the command and waits for it synchronously, up
// to foregroundPromoteAfter. If that wall-clock budget elapses before
// the command finishes, the job is registered as a background task and
// a "promoted" result is returned — the LLM gets the partial output it
// has so far plus a task_id it can poll, and the underlying command
// keeps running uninterrupted.
func (b *bashTool) runForeground(ctx context.Context, a bashArgs) *bashResult {
	job := b.spawnJob(a)

	// Set up the auto-promote timer only when the command's own timeout
	// is longer than the promotion window — otherwise the timeout would
	// fire before the promotion ever could, and queueing the timer just
	// adds noise to the select. (Call() normalises TimeoutSeconds<=0 to
	// 120 before we get here.)
	var promoteCh <-chan time.Time
	if time.Duration(a.TimeoutSeconds)*time.Second > foregroundPromoteAfter {
		promoteCh = time.After(foregroundPromoteAfter)
	}

	select {
	case <-job.doneCh:
		// Natural completion inside the synchronous budget.
		return foregroundResult(job)
	case <-ctx.Done():
		// The inbound tool-call context was cancelled (typically agent
		// shutdown). Kill the job and return whatever we managed to
		// capture.
		job.cancel()
		<-job.doneCh
		return foregroundResult(job)
	case <-promoteCh:
		// Promote to background: register the job, hand back a task_id
		// the LLM can poll. The command continues running with its
		// original timeout budget.
		id := b.registerTaskID(job)
		partial := job.snapshot()
			promoteSecs := int(foregroundPromoteAfter.Seconds())
			notice := "\n" + fmt.Sprintf(
				"<hangrix-event kind=\"notification.bash.promoted\" id=\"%s\" status=\"running\">"+
				"<promotion after_seconds=\"%d\"/>"+
				"<command>%s</command>"+
				"</hangrix-event>\n"+
				"Poll progress with bash(task_id=%q); answer prompts with bash_input(task_id=%q, data=...).",
			xmlEscape(id), promoteSecs, xmlCDATA(job.command), id, id,
			)
		return &bashResult{
			Summary:    a.Summary,
			Output:     partial + notice,
			TaskID:     id,
			Status:     "promoted",
			OutputFile: job.outPath,
		}
	}
}

func foregroundResult(job *bashJob) *bashResult {
	job.mu.Lock()
	defer job.mu.Unlock()
	// Non-exit errors (start failures, IO errors) end up in the file
	// already via the goroutines above; the exit code carries the rest
	// of the signal. We don't need to re-append anything here.
	//
	// OutputFile is set on foreground results so the unified size guard
	// (tools/result_guard.go) can reference the existing per-job log
	// instead of creating a duplicate temp file when the Output field
	// exceeds the result budget.
	return &bashResult{
		Summary:    job.summary,
		Output:     job.snapshot(),
		ExitCode:   job.exitCode,
		TimedOut:   job.timedOut,
		OutputFile: job.outPath,
	}
}

func (b *bashTool) spawnBackground(a bashArgs) *bashResult {
	job := b.spawnJob(a)
	id := b.registerTaskID(job)
	return &bashResult{
		Summary:    a.Summary,
		TaskID:     id,
		Status:     "running",
		OutputFile: job.outPath,
	}
}

// registerTaskID assigns a fresh task_id to the job, stores it in the
// jobs map, and stamps it onto the job itself so the wait goroutine can
// fire a notification when the command eventually finishes. If the job
// has *already* finished (a very-fast command that beat the registration
// path), we synchronously emit the notification right here — otherwise
// the LLM would have to poll just to learn the result of a job whose
// whole point was "don't make me wait".
func (b *bashTool) registerTaskID(job *bashJob) string {
	id := newTaskID()
	b.mu.Lock()
	b.jobs[id] = job
	b.mu.Unlock()

	job.mu.Lock()
	job.taskID = id
	alreadyDone := job.done
	job.mu.Unlock()
	if alreadyDone {
		b.notify(formatJobNotification(job))
	}
	return id
}


// xmlEscape escapes a string for safe inclusion in XML text content or
// attribute values. Uses encoding/xml.EscapeText which handles &, <, >,
// ", and '.
func xmlEscape(s string) string {
	var buf strings.Builder
	xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// xmlCDATA wraps a string in a CDATA section, handling the edge case
// where the content itself contains "]]>" by splitting the CDATA block.
func xmlCDATA(s string) string {
	safe := strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>")
	return "<![CDATA[" + safe + "]]>"
}
// formatJobNotification produces the short user-role text the runtime
// drains into the LLM context when a background bash task ends. The
// goal is "tell the model the job is done, with enough context that it
// doesn't *have* to poll" — task_id, command, exit code, elapsed,
// and the tail of the output stream. We keep it deliberately compact
// (~10 lines max) because every notification is paid for in tokens.
func formatJobNotification(job *bashJob) string {
	job.mu.Lock()
	taskID := job.taskID
	exit := job.exitCode
	timedOut := job.timedOut
	startedAt := job.startedAt
	command := job.command
	job.mu.Unlock()

	elapsed := time.Since(startedAt).Round(time.Second)

	// Trim the snapshot tail to the last ~20 lines / 2 KiB; the full
	// output is still available via bash(task_id=...) or by reading the
	// output_file directly.
	snap := job.snapshot()
	const maxTailBytes = 2048
	const maxTailLines = 20
	if len(snap) > maxTailBytes {
		snap = "…\n" + snap[len(snap)-maxTailBytes:]
	}
	if lines := strings.Split(snap, "\n"); len(lines) > maxTailLines {
		snap = "…\n" + strings.Join(lines[len(lines)-maxTailLines:], "\n")
	}
	snap = strings.TrimRight(snap, "\n")
	if snap == "" {
		snap = "(no output)"
	}

	elapsedSeconds := int(elapsed.Seconds())
	timedOutStr := "false"
	if timedOut {
		timedOutStr = "true"
	}
	return fmt.Sprintf(
		"<hangrix-event kind=\"notification.bash.finished\" id=\"%s\" status=\"done\">"+
			"<outcome exit_code=\"%d\" timed_out=\"%s\" elapsed_seconds=\"%d\"/>"+
			"<command>%s</command>"+
			"<output_tail>%s</output_tail>"+
			"</hangrix-event>",
		xmlEscape(taskID), exit, timedOutStr, elapsedSeconds,
		xmlCDATA(command), xmlCDATA(snap),
	)
}

func (b *bashTool) poll(id string) *bashResult {
	b.mu.Lock()
	job, ok := b.jobs[id]
	b.mu.Unlock()
	if !ok {
		return &bashResult{
			ExitCode: -1,
			Output: fmt.Sprintf(
				"bash: unknown task_id %q. Task ids are generated by the tool when you start a command with run_in_background=true (or when a long foreground command auto-promotes after 30s) and are valid for the lifetime of the agent process. Only pass back a task_id that bash previously returned in this session; do not invent or modify the value.",
				id,
			),
		}
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	res := &bashResult{
		Summary:    job.summary,
		Output:     job.snapshot(),
		ExitCode:   job.exitCode,
		TimedOut:   job.timedOut,
		TaskID:     id,
		OutputFile: job.outPath,
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

func newSleepID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "sleep_" + hex.EncodeToString(b[:])
}

// writeEphemeralError stashes an error message in a temp file so
// snapshot() can read it back. Only used on the (rare) path where
// os.CreateTemp itself failed and we still need the error to round-trip
// through the normal result shape.
func writeEphemeralError(msg string) string {
	f, err := os.CreateTemp("", "hangrix-bash-err-*.log")
	if err != nil {
		// If even this fails, return a sentinel path; snapshot() will
		// see ENOENT and return "" — the exit code already signals -1.
		return "/dev/null/hangrix-no-tmp"
	}
	_, _ = f.WriteString(msg)
	_ = f.Close()
	return f.Name()
}

// bashInputTool writes to the stdin of a background bash task. It exists
// because the only way to answer an interactive prompt on a long-running
// command (y/N confirmations, password fields, REPLs, anything that
// blocks on read()) is to feed bytes into the PTY master from outside.
//
// The tool intentionally shares state with bashTool — they look up the
// same job map — rather than going through a public surface. The two
// tools are co-conceived: bash hands out the task_id; bash_input is the
// only thing that can do anything useful with the stdin side of it.
type bashInputTool struct {
	bash *bashTool
}

func newBashInputTool(b *bashTool) Tool {
	return &bashInputTool{bash: b}
}

func (*bashInputTool) Name() string { return "bash_input" }
func (*bashInputTool) Description() string {
	return "Write to the stdin of a background bash task (one started with bash run_in_background=true, or one promoted from foreground after 30s). " +
		"Use this to answer interactive prompts: y/N confirmations, password fields, REPL inputs, or anything else that read()s from the terminal. " +
		"By default a trailing newline is appended (so plain inputs like 'y' or 'mypassword' submit cleanly); set no_newline=true to send raw bytes instead, e.g. when driving a TUI with control codes."
}
func (*bashInputTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id":    map[string]any{"type": "string", "description": "Opaque task_id returned by an earlier bash call (run_in_background=true or a foreground call that promoted after 30s). Mandatory — do not invent."},
			"data":       map[string]any{"type": "string", "description": "Text to write to the task's stdin."},
			"no_newline": map[string]any{"type": "boolean", "description": "Skip the auto-appended '\\n'. Default false. Set true when you need to send raw bytes (e.g. escape sequences) without a line terminator."},
		},
		"required": []string{"task_id", "data"},
	}
}

type bashInputArgs struct {
	TaskID    string `json:"task_id"`
	Data      string `json:"data"`
	NoNewline bool   `json:"no_newline"`
}

type bashInputResult struct {
	TaskID       string `json:"task_id"`
	BytesWritten int    `json:"bytes_written"`
}

func (t *bashInputTool) Call(_ context.Context, raw json.RawMessage) (any, error) {
	var a bashInputArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.TaskID == "" {
		return nil, errors.New("bash_input: missing 'task_id'. Pass the task_id returned by an earlier bash call with run_in_background=true; do not invent a value.")
	}

	t.bash.mu.Lock()
	job, ok := t.bash.jobs[a.TaskID]
	t.bash.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("bash_input: unknown task_id %q. Task ids are generated by bash when you start a command with run_in_background=true and are valid for the lifetime of the agent process. Only pass back a task_id that bash previously returned.", a.TaskID)
	}

	job.mu.Lock()
	done := job.done
	ptmx := job.ptmx
	job.mu.Unlock()
	if done {
		return nil, fmt.Errorf("bash_input: task %q has already finished; its stdin is closed. Poll the task with bash(task_id=%q) to see the final output, then start a new background command if you need to send more input.", a.TaskID, a.TaskID)
	}
	if ptmx == nil {
		return nil, fmt.Errorf("bash_input: task %q was never attached to a pty (it failed at start). Poll the task with bash(task_id=%q) to see the start error.", a.TaskID, a.TaskID)
	}

	data := a.Data
	if !a.NoNewline && !strings.HasSuffix(data, "\n") {
		data += "\n"
	}
	n, err := ptmx.Write([]byte(data))
	if err != nil {
		return nil, fmt.Errorf("bash_input: write to task %q failed: %w. The task may have just exited; poll it with bash(task_id=%q) to confirm.", a.TaskID, err, a.TaskID)
	}
	return &bashInputResult{TaskID: a.TaskID, BytesWritten: n}, nil
}
