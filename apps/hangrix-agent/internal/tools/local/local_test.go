package local_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestBashForeground covers the basic (sync) bash path: output capture
// and exit code propagation. The PTY merges stdout and stderr by design,
// so we assert against the unified `output` field rather than checking
// the streams separately. Background mode + polling is covered by the
// runtime smoke test downstream; bash_input has its own test below.
func TestBashForeground(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	resRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command": "echo out; echo err 1>&2; exit 7",
		"summary": "Print streams and exit 7",
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	// The bash result type is unexported by design — the IPC + LLM
	// round-trip is JSON, so we assert via JSON re-encode rather than
	// reaching into a private struct.
	out := mustReJSON(resRaw)
	var fields struct {
		Summary  string `json:"summary"`
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
		TimedOut bool   `json:"timed_out"`
	}
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("re-decode: %v (%s)", err, out)
	}
	// PTY merges the two streams; ordering between them is buffer-
	// dependent, so we just require that both lines made it through.
	if !strings.Contains(fields.Output, "out") {
		t.Errorf("output should contain 'out'; got %q", fields.Output)
	}
	if !strings.Contains(fields.Output, "err") {
		t.Errorf("output should contain 'err'; got %q", fields.Output)
	}
	if fields.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", fields.ExitCode)
	}
	if fields.TimedOut {
		t.Errorf("timed_out should be false")
	}
	// Summary is supplied by the LLM and echoed back unchanged — the
	// agent-log UI uses it as the collapsed-row label, so if it ever
	// stops round-tripping the chip falls back to a generic "bash".
	if fields.Summary != "Print streams and exit 7" {
		t.Errorf("summary should round-trip from the input args verbatim; got %q", fields.Summary)
	}
}

// TestBashSummaryRoundTripsOnPoll pins that the LLM-supplied summary on
// a background start is preserved across task_id polls — without it, a
// promoted job's UI chip would go blank on the next poll because the
// agent-log view rebuilds the chip from every fresh tool_call payload.
func TestBashSummaryRoundTripsOnPoll(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	startRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command":           "echo hi",
		"summary":           "Echo a greeting",
		"run_in_background": true,
	}))
	if err != nil {
		t.Fatalf("bash start: %v", err)
	}
	var start struct {
		TaskID  string `json:"task_id"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(mustReJSON(startRaw), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if start.Summary != "Echo a greeting" {
		t.Errorf("background start should echo summary verbatim; got %q", start.Summary)
	}

	// Wait for completion, then poll — summary must still be there.
	deadline := time.Now().Add(3 * time.Second)
	var poll struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	for time.Now().Before(deadline) {
		pollRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{"task_id": start.TaskID}))
		if err != nil {
			t.Fatalf("bash poll: %v", err)
		}
		if err := json.Unmarshal(mustReJSON(pollRaw), &poll); err != nil {
			t.Fatalf("decode poll: %v", err)
		}
		if poll.Status == "done" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if poll.Summary != "Echo a greeting" {
		t.Errorf("poll should carry the original summary; got %q", poll.Summary)
	}
}

// TestBashForegroundIsATTY pins the reason we bother with a PTY at all:
// `tty -s` succeeds (exit 0) when stdout is a terminal, so isatty-aware
// tools (apt, npm, anything that toggles colour or line buffering) see
// the "interactive terminal" version of themselves the same way a human
// shell would. Regress to plain pipes and this exits 1.
func TestBashForegroundIsATTY(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	resRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command": "tty -s && echo yes || echo no",
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	var fields struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal(mustReJSON(resRaw), &fields); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !strings.Contains(fields.Output, "yes") {
		t.Errorf("expected PTY-attached stdout (tty -s success); got output=%q", fields.Output)
	}
	if fields.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", fields.ExitCode)
	}
}

// TestBashForegroundKillsBackgroundedGrandchild pins two things:
//   - The call returns promptly even when the script backgrounds a
//     long-running grandchild (`./srv &`). Without WaitDelay, exec would
//     hang on the inherited stdout/stderr pipe FDs until the timeout.
//   - The grandchild is actually killed, not just abandoned. We verify
//     by giving the grandchild a delayed `touch <marker>` and asserting
//     the marker never appears. If Setpgid + group-kill regressed, the
//     orphaned grandchild would survive and write the marker.
func TestBashForegroundKillsBackgroundedGrandchild(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")

	start := time.Now()
	resRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		// Grandchild sleeps 5s before touching the marker. The bash leader
		// exits immediately after `echo ready`, so Call should return ~2s
		// later (WaitDelay) and SIGKILL the group on the way out. If the
		// group-kill works, the grandchild dies before the 5s sleep ends
		// and the marker is never created.
		"command":         "( sleep 5 && touch " + marker + " ) &\necho ready",
		"timeout_seconds": 20,
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Fatalf("bash hung waiting for backgrounded grandchild: took %v (want < 10s)", elapsed)
	}
	out := mustReJSON(resRaw)
	var fields struct {
		Output   string `json:"output"`
		TimedOut bool   `json:"timed_out"`
	}
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("re-decode: %v (%s)", err, out)
	}
	if !strings.Contains(fields.Output, "ready") {
		t.Errorf("output should contain 'ready'; got %q", fields.Output)
	}
	if fields.TimedOut {
		t.Errorf("should not have timed out (WaitDelay should fire first)")
	}

	// Sleep past the grandchild's 5s timer; if it survived, the marker
	// shows up here.
	time.Sleep(6 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("grandchild survived group-kill: marker %q exists", marker)
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

// TestBashAutoPromoteToBackground is the headline test for the 30s
// promotion rule: a synchronous call to a slow command must return
// promptly with a task_id and status="promoted" instead of blocking the
// whole turn. We shorten the threshold via the test hook so the suite
// doesn't have to wait 30 seconds.
//
// Regression target: if the promotion path silently degrades to either
// (a) blocking until the command finishes, or (b) killing the command
// at the threshold, long apt/build/test runs will stop being usable.
func TestBashAutoPromoteToBackground(t *testing.T) {
	// Not parallel: this test mutates package-level promotion threshold.
	restore := local.SetForegroundPromoteAfterForTest(200 * time.Millisecond)
	defer restore()

	tools := byName(local.All())
	bash := tools["bash"]

	start := time.Now()
	resRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		// Sleep past the promotion threshold, then write a final marker
		// that only the post-promotion poll can see.
		"command":         "sleep 1; echo finished-after-promote",
		"timeout_seconds": 10,
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	elapsed := time.Since(start)
	// The call must return shortly after the (200ms) threshold — we
	// give a generous 2s ceiling so CI scheduling jitter doesn't make
	// this flaky. Anything more than that suggests we're waiting for
	// the command to finish synchronously.
	if elapsed > 2*time.Second {
		t.Fatalf("foreground call did not promote in time: took %v", elapsed)
	}

	var promo struct {
		Status     string `json:"status"`
		TaskID     string `json:"task_id"`
		Output     string `json:"output"`
		OutputFile string `json:"output_file"`
	}
	if err := json.Unmarshal(mustReJSON(resRaw), &promo); err != nil {
		t.Fatalf("decode promote: %v", err)
	}
	if promo.Status != "promoted" {
		t.Fatalf("status = %q, want %q", promo.Status, "promoted")
	}
	if promo.TaskID == "" {
		t.Fatalf("expected a task_id on promotion; got %+v", promo)
	}
	if promo.OutputFile == "" {
		t.Errorf("expected output_file path on promotion; got %+v", promo)
	}
	// The notice must point the LLM at how to continue — bash poll and
	// bash_input — otherwise the model has to guess.
	for _, want := range []string{"promoted", "task_id", "bash_input"} {
		if !strings.Contains(promo.Output, want) {
			t.Errorf("promotion notice should mention %q; got: %s", want, promo.Output)
		}
	}

	// Poll until the underlying command actually finishes. The marker
	// "finished-after-promote" only appears *after* the sleep, so this
	// also proves the command kept running past promotion.
	deadline := time.Now().Add(5 * time.Second)
	var poll struct {
		Status     string `json:"status"`
		Output     string `json:"output"`
		ExitCode   int    `json:"exit_code"`
		OutputFile string `json:"output_file"`
	}
	for time.Now().Before(deadline) {
		pollRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
			"task_id": promo.TaskID,
		}))
		if err != nil {
			t.Fatalf("bash poll: %v", err)
		}
		if err := json.Unmarshal(mustReJSON(pollRaw), &poll); err != nil {
			t.Fatalf("decode poll: %v", err)
		}
		if poll.Status == "done" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if poll.Status != "done" {
		t.Fatalf("promoted task did not finish: status=%q output=%q", poll.Status, poll.Output)
	}
	if poll.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; output=%q", poll.ExitCode, poll.Output)
	}
	if !strings.Contains(poll.Output, "finished-after-promote") {
		t.Errorf("promoted command should have run to completion; final output=%q", poll.Output)
	}
	if poll.OutputFile != promo.OutputFile {
		t.Errorf("output_file changed across poll: promotion=%q poll=%q", promo.OutputFile, poll.OutputFile)
	}

	// The output_file is supposed to be readable directly — that's why
	// we surface it. If a third party (e.g. `tail -f` from a follow-up
	// bash call) couldn't see what poll just returned, the contract is
	// broken.
	body, err := os.ReadFile(poll.OutputFile)
	if err != nil {
		t.Fatalf("read output_file %q: %v", poll.OutputFile, err)
	}
	if !strings.Contains(string(body), "finished-after-promote") {
		t.Errorf("output_file %q does not contain the marker; got %q", poll.OutputFile, body)
	}
}

// TestBashBackgroundExposesOutputFile pins the contract that
// run_in_background=true returns an output_file path the LLM can read
// directly (e.g. `tail -f` via a sibling bash call). Without this, the
// model is stuck polling — which is fine for short bursts but useless
// for tailing a long build log.
func TestBashBackgroundExposesOutputFile(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]

	startRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command":           "for i in 1 2 3; do echo tick-$i; sleep 0.05; done",
		"run_in_background": true,
	}))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	var start struct {
		TaskID     string `json:"task_id"`
		Status     string `json:"status"`
		OutputFile string `json:"output_file"`
	}
	if err := json.Unmarshal(mustReJSON(startRaw), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if start.Status != "running" {
		t.Errorf("status = %q, want %q", start.Status, "running")
	}
	if start.OutputFile == "" {
		t.Fatal("background start should include an output_file path")
	}
	// The path must exist immediately — even if the writer hasn't
	// flushed anything yet, the file is opened in spawnJob.
	if _, err := os.Stat(start.OutputFile); err != nil {
		t.Fatalf("output_file %q should exist on start: %v", start.OutputFile, err)
	}

	// Wait for the task to complete, then read the file directly (not
	// via poll). This is the "tail it from another shell" path.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pollRaw, _ := bash.Call(context.Background(), mustJSON(map[string]any{"task_id": start.TaskID}))
		var pf struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(mustReJSON(pollRaw), &pf)
		if pf.Status == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	body, err := os.ReadFile(start.OutputFile)
	if err != nil {
		t.Fatalf("read output_file: %v", err)
	}
	for _, want := range []string{"tick-1", "tick-2", "tick-3"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("output_file should contain %q; got %q", want, body)
		}
	}
}

// TestBashInputAnswersInteractivePrompt is the headline test for
// bash_input — it pins the end-to-end flow the tool exists to enable:
// start a background bash task that blocks on read(), call bash_input
// with its task_id to feed it an answer, then confirm the program
// completed and the answer reached it. If this regresses, interactive
// confirmations stop working and the LLM is back to dodging y/N prompts
// with `yes |` hacks.
func TestBashInputAnswersInteractivePrompt(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]
	bashInput := tools["bash_input"]
	if bashInput == nil {
		t.Fatal("bash_input tool not registered")
	}

	// Start a background bash that prints a prompt, reads a single line,
	// then echoes "got=<answer>" and exits. Anything that blocks on read
	// would work here; we use plain `read` because it's the canonical
	// shape of a y/N or password prompt.
	startRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command":           "printf 'continue? '; read ans; echo got=$ans",
		"run_in_background": true,
		"timeout_seconds":   15,
	}))
	if err != nil {
		t.Fatalf("bash start: %v", err)
	}
	var start struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(mustReJSON(startRaw), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if start.TaskID == "" {
		t.Fatalf("expected a task_id on background start; got %+v", start)
	}
	if start.Status != "running" {
		t.Errorf("expected status=running on background start; got %q", start.Status)
	}

	// Give the child a moment to actually reach the read() call. Without
	// this the input can race ahead of the prompt and the test becomes
	// flaky in CI. 200ms is plenty for a bash one-liner.
	time.Sleep(200 * time.Millisecond)

	inRaw, err := bashInput.Call(context.Background(), mustJSON(map[string]any{
		"task_id": start.TaskID,
		"data":    "yes",
	}))
	if err != nil {
		t.Fatalf("bash_input: %v", err)
	}
	var inFields struct {
		BytesWritten int    `json:"bytes_written"`
		TaskID       string `json:"task_id"`
	}
	if err := json.Unmarshal(mustReJSON(inRaw), &inFields); err != nil {
		t.Fatalf("decode bash_input: %v", err)
	}
	if inFields.TaskID != start.TaskID {
		t.Errorf("bash_input echoed task_id %q, want %q", inFields.TaskID, start.TaskID)
	}
	// "yes" + auto-appended "\n" = 4 bytes. If we ever stop appending the
	// newline by default, callers' prompts will silently hang and this
	// guard surfaces the change.
	if inFields.BytesWritten != 4 {
		t.Errorf("bytes_written = %d, want 4 (auto-appended newline)", inFields.BytesWritten)
	}

	// Poll until the task finishes. Bounded loop so a regression doesn't
	// hang the suite — the underlying command should be done in well
	// under a second.
	deadline := time.Now().Add(5 * time.Second)
	var pollFields struct {
		Output   string `json:"output"`
		Status   string `json:"status"`
		ExitCode int    `json:"exit_code"`
	}
	for time.Now().Before(deadline) {
		pollRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
			"task_id": start.TaskID,
		}))
		if err != nil {
			t.Fatalf("bash poll: %v", err)
		}
		if err := json.Unmarshal(mustReJSON(pollRaw), &pollFields); err != nil {
			t.Fatalf("decode poll: %v", err)
		}
		if pollFields.Status == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pollFields.Status != "done" {
		t.Fatalf("task did not reach done: status=%q output=%q", pollFields.Status, pollFields.Output)
	}
	if pollFields.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", pollFields.ExitCode)
	}
	if !strings.Contains(pollFields.Output, "got=yes") {
		t.Errorf("output should echo the answer ('got=yes'); got %q", pollFields.Output)
	}
}

// TestBashInputAfterDone pins the error path: writing to a task that has
// already exited should fail with a message the LLM can self-correct
// from, not silently swallow the bytes.
func TestBashInputAfterDone(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bash := tools["bash"]
	bashInput := tools["bash_input"]

	startRaw, err := bash.Call(context.Background(), mustJSON(map[string]any{
		"command":           "echo hi",
		"run_in_background": true,
	}))
	if err != nil {
		t.Fatalf("bash start: %v", err)
	}
	var start struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(mustReJSON(startRaw), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}

	// Wait for the task to finish so the stdin side is definitely closed.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pollRaw, _ := bash.Call(context.Background(), mustJSON(map[string]any{"task_id": start.TaskID}))
		var pf struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(mustReJSON(pollRaw), &pf)
		if pf.Status == "done" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	_, err = bashInput.Call(context.Background(), mustJSON(map[string]any{
		"task_id": start.TaskID,
		"data":    "y",
	}))
	if err == nil {
		t.Fatal("expected an error when writing to a finished task")
	}
	if !strings.Contains(err.Error(), "finished") && !strings.Contains(err.Error(), "closed") {
		t.Errorf("error should explain why the write failed; got: %v", err)
	}
}

// TestBashInputUnknownTaskID pins the recovery message for the case the
// LLM is most likely to hit: invented or stale task_id. We want the
// error to tell it where task_ids come from so it doesn't keep retrying.
func TestBashInputUnknownTaskID(t *testing.T) {
	t.Parallel()
	tools := byName(local.All())
	bashInput := tools["bash_input"]

	_, err := bashInput.Call(context.Background(), mustJSON(map[string]any{
		"task_id": "task_deadbeef",
		"data":    "y",
	}))
	if err == nil {
		t.Fatal("expected an error for an unknown task_id")
	}
	for _, want := range []string{"unknown task_id", "run_in_background"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q (so the LLM knows how to recover); got: %v", want, err)
		}
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
