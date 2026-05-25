package runtime

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
	"strings"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/ipc"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
)

const atMentionReminder = "<system_reminder>Your last assistant message contains an `@`. If you meant to mention another role (e.g. `@agent-<role-key>`) or post to the issue thread, call the `issue_comment` tool and put the text in its `body` argument — plain assistant text is recorded on the session timeline but does NOT wake other roles or post a comment. If the `@` was incidental (an email address, a code snippet), ignore this reminder and continue.</system_reminder>"

// Loop owns the message-pump that ties IPC, LLM and tools together.
// It reads inbound frames from `in`, drives the LLM, dispatches tool
// calls, and emits outbound frames on `out`. Single-instance; not safe
// to call Run concurrently with itself.
//
// Lifecycle. The agent is long-lived: one container processes events
// one after another until the runner either retires it (control:shutdown
// inbound) or the stdin pipe closes. After each event finishes, the loop
// emits an `idle` outbound frame and parks itself on the inbox waiting
// for either the next event or an async notification.
//
// Inbox model. Everything the loop reacts to — IPC frames from the
// runner, background-task completion notifications from the bash tool —
// fans into one buffered channel and is consumed via select. This is the
// only way to guarantee a notification raised mid-LLM-call still lands
// in the LLM's context: the LLM call runs in a goroutine, the main
// goroutine selects on (resp, inbox, ctx.Done) at the same level. Frames
// that arrive mid-event (a second event queued behind the current one)
// are buffered into pendingFrames and replayed at the next idle.
type Loop struct {
	in       *ipc.Reader
	out      *ipc.Writer
	llm      *llm.Client
	model    string
	registry *tools.Registry
	system   string
	async    local.AsyncLifecycle

	// maxToolRounds caps how many LLM⇄tool round-trips we allow within
	// one inbound event. The cap exists only as a runaway-loop fail-safe;
	// in practice an agent should never approach it. Lifted to a very
	// large number so legitimate long sessions (refactors that touch
	// many files, multi-step debugging) don't get cut off mid-stream.
	maxToolRounds int

	// shutdownGrace bounds how long Cleanup waits for async work
	// (background bash tasks, sleep timers) to finish on shutdown before
	// we exit anyway. The container teardown will reap whatever is left;
	// this just keeps the exit path from wedging.
	shutdownGrace time.Duration

	// compactTokenThreshold is the input-token usage above which the
	// loop injects a synthetic system reminder telling the LLM to call
	// compact_session at the next safe boundary. 0 disables the nudge —
	// the LLM still decides on its own when compact_session is right.
	compactTokenThreshold int

	// lastInputTokens is the most recent CreateResponse.Usage.InputTokens.
	// We sample it after every LLM call so the compact-threshold check
	// runs against the actual prompt size we just paid for, not an
	// estimate. Reset to 0 each time compact_session lands so a single
	// crossing doesn't fire the nudge twice.
	lastInputTokens int

	// compactNudged guards against repeatedly injecting the same
	// "compact your session now" reminder turn after turn while the
	// LLM is still mid-task and hasn't reached a point where it can
	// safely call compact_session. We arm it once when the threshold
	// is first crossed and disarm it after the next compact_session
	// invocation OR a hard reset of the context window.
	compactNudged bool

	// reasoningTimeout is the per-call wall-clock ceiling for a single
	// llm.Create() invocation. When exceeded the agent cancels the
	// request and — if retries remain — retries with the same snapshot.
	// <=0 disables this protection.
	reasoningTimeout time.Duration
	// reasoningTimeoutRetries is the number of retries AFTER the first
	// timeout (total attempts = retries + 1). Only reasoning-timeout
	// errors are retried at this level.
	reasoningTimeoutRetries int
}

func NewLoop(
	in *ipc.Reader,
	out *ipc.Writer,
	llmClient *llm.Client,
	model string,
	registry *tools.Registry,
	systemPrompt string,
	async local.AsyncLifecycle,
	compactTokenThreshold int,
		reasoningTimeout time.Duration,
		reasoningTimeoutRetries int,
) *Loop {
	return &Loop{
		in:                      in,
		out:                     out,
		llm:                     llmClient,
		model:                   model,
		registry:                registry,
		system:                  systemPrompt,
		async:                   async,
		maxToolRounds:           999999,
		shutdownGrace:           5 * time.Second,
		compactTokenThreshold:   compactTokenThreshold,
		reasoningTimeout:        reasoningTimeout,
		reasoningTimeoutRetries: reasoningTimeoutRetries,
	}
}

// inboxItem is one thing the loop reacts to. Exactly one of Frame /
// Notification is populated per item. The reader goroutine (stdin) and
// the async-lifecycle notification goroutine both push into the same
// channel — the loop never has to multiplex sources itself.
type inboxItem struct {
	Frame        *ipc.Inbound
	Notification string
	ReaderErr    error // set when the stdin reader itself fails (EOF, decode error)
}

// Run blocks until shutdown or stdin closes. The first inbound frame
// MUST be `history` — a brand-new session sends `history` with an empty
// messages array. We treat a non-history first frame as a runner bug and
// bail; the alternative (proceeding with empty history) would cause the
// agent to hallucinate prior context on a re-spawn after a crash.
func (l *Loop) Run(ctx context.Context) error {
	first, err := l.in.Read()
	if err != nil {
		return fmt.Errorf("runtime: read first frame: %w", err)
	}
	if first.Kind != "history" {
		return fmt.Errorf("runtime: first frame must be kind=history, got %q", first.Kind)
	}
	history := historyToMessages(first.Messages)
	cctx := NewContext(l.system, history)

	_ = l.out.Status("ready")

	// Start the inbox pumps. The stdin reader runs forever (until EOF /
	// decode error / ctx cancel); the bash notification subscriber
	// blocks on b.NotificationCh() and forwards each completion record
	// into the same inbox. Closing them on shutdown is implicit — the
	// goroutines exit when their sources close or ctx cancels.
	inbox := make(chan inboxItem, 64)
	go l.pumpFrames(ctx, inbox)
	go l.pumpNotifications(ctx, inbox)

	// pendingFrames holds non-event frames that arrived while we were
	// busy with an event. We replay them at the next idle so a queued
	// shutdown isn't lost to the order of events; queued event frames
	// are similarly replayed and processed back-to-back.
	var pendingFrames []*ipc.Inbound

	for {
		// Replay any frames the inner loop deferred. They go through
		// the same handler as freshly-received frames so the control /
		// event / history switch only lives in one place.
		if len(pendingFrames) > 0 {
			next := pendingFrames[0]
			pendingFrames = pendingFrames[1:]
			done, err := l.handleFrame(ctx, cctx, next, inbox, &pendingFrames)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case item, ok := <-inbox:
			if !ok {
				// Inbox closed = stdin shut + notifier shut. Nothing
				// more can drive us; exit cleanly.
				return nil
			}
			switch {
			case item.ReaderErr != nil:
				if errors.Is(item.ReaderErr, io.EOF) {
					return nil
				}
				return fmt.Errorf("runtime: read inbound: %w", item.ReaderErr)
			case item.Frame != nil:
				done, err := l.handleFrame(ctx, cctx, item.Frame, inbox, &pendingFrames)
				if err != nil {
					return err
				}
				if done {
					return nil
				}
			case item.Notification != "":
				// A notification arrived while idle: drive an event-style
				// turn so the LLM gets a chance to react. The notification
				// text becomes a user-role message; the LLM may poll a
				// task_id, send bash_input, or simply acknowledge before
				// returning to idle.
				cctx.AppendUser(item.Notification)
				if err := l.driveOneTurn(ctx, cctx, inbox, &pendingFrames); err != nil {
					return err
				}
				if err := l.emitIdle(); err != nil {
					return err
				}
			}
		}
	}
}

// handleFrame dispatches a single inbound frame. Returns (true, nil)
// when the frame caused the loop to terminate (e.g. control:shutdown).
// pendingFrames is the spillover slice for non-event frames the inner
// event loop deferred — handleFrame appends to it, never reads from it
// (Run drains it before the next select).
func (l *Loop) handleFrame(
	ctx context.Context,
	cctx *Context,
	frame *ipc.Inbound,
	inbox <-chan inboxItem,
	pendingFrames *[]*ipc.Inbound,
) (done bool, err error) {
	switch frame.Kind {
	case "control":
		if frame.Op == "shutdown" {
			l.gracefulShutdown()
			return true, nil
		}
		_ = l.out.Log("warn", fmt.Sprintf("unknown control op: %s", frame.Op))
		return false, nil
	case "event":
		if err := l.handleEvent(ctx, cctx, frame, inbox, pendingFrames); err != nil {
			return false, err
		}
		if err := l.emitIdle(); err != nil {
			return false, err
		}
		return false, nil
	case "history":
		// Second history frame is a re-sync (runner re-attaching after
		// agent restart). Replace the working context with the runner's
		// authoritative copy and emit ready so the runner knows we're
		// caught up.
		*cctx = *NewContext(l.system, historyToMessages(frame.Messages))
		_ = l.out.Status("ready")
		return false, nil
	default:
		_ = l.out.Log("warn", fmt.Sprintf("unknown inbound kind: %s", frame.Kind))
		return false, nil
	}
}

// gracefulShutdown is the control:shutdown handler. We cancel every
// alive background bash task, wait up to shutdownGrace for them to
// reap, then return so Run can exit. Container teardown will sweep
// whatever is left.
func (l *Loop) gracefulShutdown() {
	if l.async == nil {
		return
	}
	if l.async.HasRunningJobs() == 0 {
		return
	}
	gctx, cancel := context.WithTimeout(context.Background(), l.shutdownGrace)
	defer cancel()
	l.async.Cleanup(gctx)
}

// emitIdle reports event-done state to the runner along with a hint
// about how many async work items (bash jobs + sleep timers) are still
// alive. The runner uses running_jobs to pick the right idle timeout
// (don't retire a container that's babysitting a long test run).
func (l *Loop) emitIdle() error {
	n := 0
	if l.async != nil {
		n = l.async.HasRunningJobs()
	}
	return l.out.Idle(n)
}

func (l *Loop) handleEvent(
	ctx context.Context,
	cctx *Context,
	frame *ipc.Inbound,
	inbox <-chan inboxItem,
	pendingFrames *[]*ipc.Inbound,
) error {
	turnID := newTurnID()
	eventMsg, err := renderEventMessage(frame.Event, frame.Payload)
	if err != nil {
		_ = l.out.Log("error", fmt.Sprintf("render event: %s", err))
		_ = l.out.Done(turnID)
		// Render failure means the inbound frame was malformed; that's
		// a platform-side bug, not a transient issue worth retrying.
		// Bubble up so the runner records the session as failed.
		return fmt.Errorf("render event: %w", err)
	}
	cctx.AppendUser(eventMsg)
	return l.driveOneTurnWithID(ctx, cctx, inbox, pendingFrames, turnID)
}

// driveOneTurn runs the LLM ⇄ tool round-trip loop until the assistant
// returns no tool calls (event finished) or the cap is reached. Used
// when an idle-state notification kicks off a turn without an inbound
// event frame.
func (l *Loop) driveOneTurn(
	ctx context.Context,
	cctx *Context,
	inbox <-chan inboxItem,
	pendingFrames *[]*ipc.Inbound,
) error {
	return l.driveOneTurnWithID(ctx, cctx, inbox, pendingFrames, newTurnID())
}

func (l *Loop) driveOneTurnWithID(
	ctx context.Context,
	cctx *Context,
	inbox <-chan inboxItem,
	pendingFrames *[]*ipc.Inbound,
	turnID string,
) error {
	// atMentionNudged is scoped per-turn so a fresh event can re-arm the
	// reminder. We fire at most once within a turn to avoid wedging the
	// loop on a model that keeps echoing `@` in plain text.
	atMentionNudged := false
	for round := 0; round < l.maxToolRounds; round++ {
		// Drain anything the inbox accumulated since the last round
		// boundary. Non-blocking — we only consume what's already
		// queued. Events and notifications fold straight into the
		// context so the LLM sees them this round; control / history
		// frames defer to the outer loop.
		l.drainPending(cctx, inbox, pendingFrames)

		// Threshold-based compact nudge. Fires once per crossing —
		// armed when the last LLM call's input-token count went above
		// the configured threshold, disarmed by an actual
		// compact_session call. The nudge is a synthetic user-role
		// reminder; the LLM is still free to finish its current
		// sub-step before calling the tool. We deliberately do NOT
		// force a compact here — interrupting a mid-task LLM with a
		// hard cutover wastes whatever decisions it was carrying.
		l.maybeNudgeCompact(cctx)

		// Snapshot the message count so we can detect whether a new
		// event or notification was folded in while the LLM was busy.
		// If it was and the LLM returns no tool calls, we need to give
		// it another round to react instead of declaring the turn done.
		preCallLen := cctx.Len()

		_ = l.out.Status("thinking")
		req := &llm.CreateRequest{
			Model:        l.model,
			Instructions: cctx.SystemPrompt(),
			Messages:     cctx.Snapshot(),
			Tools:        l.registry.Catalog(),
		}

		// Run the LLM call in its own goroutine so the main goroutine
		// can keep draining the inbox. An event or notification arriving
		// mid-call is appended to the context but does NOT cancel the
		// call — canceling would waste tokens, and the new input will be
		// visible at the next round.
		respCh := make(chan llmResult, 1)
		go func() {
			resp, err := l.llm.Create(ctx, req)
			respCh <- llmResult{resp: resp, err: err}
		}()

		var (
			resp    *llm.CreateResponse
			callErr error
		)
		waitForResp := true
		for waitForResp {
			select {
			case <-ctx.Done():
				// Process-wide cancellation. Let the in-flight LLM call
				// settle (it inherits the same ctx and will return
				// soon), but we don't surface a partial response.
				return ctx.Err()
			case r := <-respCh:
				resp = r.resp
				callErr = r.err
				waitForResp = false
			case item, ok := <-inbox:
				if !ok {
					// Inbox closed mid-call. Let the LLM call finish
					// so we can record its result, then exit normally
					// on the next outer-loop iteration.
					r := <-respCh
					resp = r.resp
					callErr = r.err
					waitForResp = false
					continue
				}
				l.applyInboxItem(cctx, item, pendingFrames)
			}
		}

		// Catch anything that landed between respCh receiving and now —
		// otherwise an event queued in the final nanoseconds of the call
		// would only be visible *after* the no-tool-call termination
		// check below, which would silently drop it into the next turn.
		l.drainPending(cctx, inbox, pendingFrames)
		postCallLen := cctx.Len()

		if callErr != nil {
			// llm.Client already retries on transport/5xx/429 with
			// exponential backoff; an error here is the upstream's
			// last word. Surface it on stdout (visible in the audit
			// log) AND on stderr (caught by runner's exit-code path)
			// so the session ends 'failed', not 'succeeded'.
			_ = l.out.Log("error", fmt.Sprintf("llm: %s", callErr))
			_ = l.out.Done(turnID)
			fmt.Fprintf(os.Stderr, "llm call failed after retries: %s\n", callErr)
			return fmt.Errorf("llm call failed: %w", callErr)
		}

		// Preserve `reasoning` blocks if the upstream emitted any. Some
		// providers (DeepSeek-Reasoner, OpenAI o-series) reject the next
		// turn if the prior reasoning_content was elided from history.
		if resp.Reasoning != "" || resp.ReasoningSignature != "" {
			cctx.AppendAssistantWithReasoning(resp.Content, resp.Reasoning, resp.ReasoningSignature, resp.ToolCalls)
		} else {
			cctx.AppendAssistant(resp.Content, resp.ToolCalls)
		}
		_ = l.out.Message("assistant", resp.Content, toIPCToolCalls(resp.ToolCalls))
		l.lastInputTokens = resp.Usage.InputTokens

		// Plain assistant text containing `@` is almost always a model
		// mistake — @agent-<role-key> mentions only wake other roles when
		// posted through the issue_comment tool. Inject a one-shot
		// reminder and force another round so the model can retry via
		// the tool. Fires at most once per turn (see atMentionNudged
		// docstring above) so a model that ignores us doesn't wedge the
		// loop in an infinite re-prompt.
		//
		// CRITICAL: when the assistant message also carries tool_calls,
		// the reminder must NOT be appended between the assistant entry
		// and its tool results — upstream rejects an assistant(tool_calls)
		// item whose results are not the immediately-following messages.
		// Defer the nudge to after the tool-dispatch loop in that case.
		shouldNudgeAtMention := !atMentionNudged && strings.ContainsRune(resp.Content, '@')
		nudgedAtMentionThisRound := false
		if shouldNudgeAtMention && len(resp.ToolCalls) == 0 {
			cctx.AppendUser(atMentionReminder)
			atMentionNudged = true
			nudgedAtMentionThisRound = true
		}

		if len(resp.ToolCalls) == 0 {
			// New user-side input arrived during the call (folded event
			// or background-task notification), or we just injected an
			// at-mention reminder. Give the LLM another pass with the
			// new input visible before closing the turn.
			if postCallLen > preCallLen || nudgedAtMentionThisRound {
				continue
			}
			_ = l.out.Done(turnID)
			return nil
		}

		// Sleep-gate: sleep is an async tool that returns immediately with
		// "scheduled" — the LLM must NOT batch it with other calls because
		// executing those calls alongside it would effectively bypass the
		// wait. If sleep is present in this batch, only execute sleep
		// (and any other sleep calls) and reject the rest with an error
		// telling the LLM to re-issue after the wake-up notification.
		if hasSleepCall(resp.ToolCalls) {
			for _, call := range resp.ToolCalls {
				_ = l.out.Status("tool")
				args := json.RawMessage(call.Arguments)
				var result tools.CallResult
				if call.Name == local.SleepToolName {
					result = l.registry.Call(ctx, call.Name, args)
				} else {
					errBody, _ := json.Marshal(map[string]any{
						"error": "This tool call was batched with sleep in the same response. Sleep must be the only call in its batch — re-issue this call in a subsequent turn after the sleep notification wakes you.",
					})
					result = tools.CallResult{
						Source:     tools.SourceLocal,
						ResultJSON: errBody,
						IsError:    true,
						ErrMsg:     "batched with sleep; re-issue after wake-up",
					}
				}
				toolContent := toolPayload(result)
				cctx.AppendToolResult(call.ID, toolContent)
				_ = l.out.ToolCall(call.ID, call.Name, args, json.RawMessage(toolContent))
			}
			// Deferred @-mention nudge: tool results are now in place, so the
			// assistant(tool_calls) → tool(result)+ chain is intact and it's
			// safe to append the reminder as the next user-role message.
			if shouldNudgeAtMention {
				cctx.AppendUser(atMentionReminder)
			}
			_ = l.out.Done(turnID)
			return nil
		}

		// pendingSummary defers the AppendSummary call until every tool
		// result in this round has been placed. Snapshot anchors on the
		// most recent KindSummary entry, so placing the summary marker
		// LAST in the round guarantees the next LLM window never starts
		// with an orphan tool message — even if the LLM (against the
		// tool's instruction) batched compact_session with another call.
		var pendingSummary string
		for _, call := range resp.ToolCalls {
			_ = l.out.Status("tool")
			args := json.RawMessage(call.Arguments)
			var result tools.CallResult
			if call.Name == local.CompactSessionToolName {
				summary, sresult := dispatchCompactSession(args)
				result = sresult
				if summary != "" {
					pendingSummary = summary
				}
			} else {
				result = l.registry.Call(ctx, call.Name, args)
			}
			toolContent := toolPayload(result)
			cctx.AppendToolResult(call.ID, toolContent)
			_ = l.out.ToolCall(call.ID, call.Name, args, json.RawMessage(toolContent))
		}
		if pendingSummary != "" {
			cctx.AppendSummary(pendingSummary)
			// Disarm the compact-threshold nudge — the LLM just did
			// the thing we were asking for. lastInputTokens stays as
			// the pre-compact reading; the next LLM round will
			// overwrite it with the post-compact (much smaller) prompt
			// size and the nudge can re-arm naturally if growth resumes.
			l.compactNudged = false
			_ = l.out.Log("info", "compact_session: session memory compacted")
		}
		// Deferred @-mention nudge: tool results are now in place, so the
		// assistant(tool_calls) → tool(result)+ chain is intact and it's
		// safe to append the reminder as the next user-role message.
		if shouldNudgeAtMention {
			cctx.AppendUser(atMentionReminder)
			atMentionNudged = true
		}
	}

	_ = l.out.Log("warn", fmt.Sprintf("max tool rounds (%d) exhausted", l.maxToolRounds))
	_ = l.out.Done(turnID)
	return nil
}

// drainPending consumes whatever is already buffered on the inbox
// without blocking, routing each item via applyInboxItem.
func (l *Loop) drainPending(cctx *Context, inbox <-chan inboxItem, pendingFrames *[]*ipc.Inbound) {
	for {
		select {
		case item, ok := <-inbox:
			if !ok {
				return
			}
			l.applyInboxItem(cctx, item, pendingFrames)
		default:
			return
		}
	}
}

// applyInboxItem routes a single inbox item. Events and notifications
// are folded directly into the LLM context so they are visible on the
// next round of the current turn — this is the seam that lets a new
// event piggy-back into an in-progress tool-call loop instead of waiting
// for the loop to finish. Control / history frames have outer-loop
// semantics (state reset, lifecycle) that don't compose with the middle
// of a turn, so they defer.
func (l *Loop) applyInboxItem(cctx *Context, item inboxItem, pendingFrames *[]*ipc.Inbound) {
	switch {
	case item.ReaderErr != nil:
		// stdin failure — defer for the outer loop so the EOF / error
		// lands in the same place as any other reader error.
		*pendingFrames = append(*pendingFrames, &ipc.Inbound{Kind: "__reader_err__"})
	case item.Frame != nil:
		if item.Frame.Kind == "event" {
			msg, err := renderEventMessage(item.Frame.Event, item.Frame.Payload)
			if err != nil {
				// Malformed payload from the runner; log and drop. We
				// don't defer it to the outer loop because that path
				// also calls renderEventMessage and would just produce
				// the same error.
				_ = l.out.Log("error", fmt.Sprintf("render event mid-turn: %s", err))
				return
			}
			cctx.AppendUser(msg)
			return
		}
		// control / history / unknown kinds — defer to the outer loop.
		*pendingFrames = append(*pendingFrames, item.Frame)
	case item.Notification != "":
		// Background task finished. Append to the context so it's
		// visible on the next round; the in-flight call (if any) is
		// not cancelled — we don't want to waste tokens.
		cctx.AppendUser(item.Notification)
	}
}

// pumpFrames reads from stdin and forwards each frame into the inbox.
// Exits on stdin EOF / decode error / ctx cancel; reports the reason
// via ReaderErr so the main loop can decide whether to treat it as a
// clean exit (EOF) or a failure.
func (l *Loop) pumpFrames(ctx context.Context, inbox chan<- inboxItem) {
	for {
		if ctx.Err() != nil {
			return
		}
		frame, err := l.in.Read()
		if err != nil {
			select {
			case inbox <- inboxItem{ReaderErr: err}:
			case <-ctx.Done():
			}
			return
		}
		select {
		case inbox <- inboxItem{Frame: frame}:
		case <-ctx.Done():
			return
		}
	}
}

// pumpNotifications forwards async-lifecycle completion notifications
// (background bash tasks, sleep timer expiry, etc.) into the inbox. The
// channel from NotificationCh() is buffered on the lifecycle side too;
// this goroutine just adapts it into the unified inbox shape so the loop
// doesn't have to select on N source-specific channels.
func (l *Loop) pumpNotifications(ctx context.Context, inbox chan<- inboxItem) {
	if l.async == nil {
		return
	}
	ch := l.async.NotificationCh()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			select {
			case inbox <- inboxItem{Notification: msg}:
			case <-ctx.Done():
				return
			}
		}
	}
}

type llmResult struct {
	resp *llm.CreateResponse
	err  error
}

// renderEventMessage formats the inbound event into a single
// user-role message. JSON wrapping is intentional: the LLM is good at
// seeing structured data in the conversation and prompts that name a
// specific event tend to invoke role-appropriate behaviour reliably.
func renderEventMessage(event string, payload json.RawMessage) (string, error) {
	payloadStr := "{}"
	if len(payload) > 0 {
		var p any
		if err := json.Unmarshal(payload, &p); err != nil {
			return "", err
		}
		compact, err := json.Marshal(p)
		if err != nil {
			return "", err
		}
		var buf strings.Builder
		xml.EscapeText(&buf, compact)
		payloadStr = buf.String()
	}
	return fmt.Sprintf(
		`<hangrix-event kind="platform.%s"><payload>%s</payload></hangrix-event>`,
		event, payloadStr,
	), nil
}

func toolPayload(r tools.CallResult) string {
	if r.IsError {
		out, _ := json.Marshal(map[string]any{"error": r.ErrMsg})
		return string(out)
	}
	if len(r.ResultJSON) == 0 {
		return "null"
	}
	return string(r.ResultJSON)
}

func toIPCToolCalls(in []llm.ToolCall) []ipc.ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]ipc.ToolCall, len(in))
	for i, c := range in {
		out[i] = ipc.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
	}
	return out
}

func historyToMessages(items []ipc.HistoryItem) []llm.Message {
	out := make([]llm.Message, 0, len(items))
	for _, it := range items {
		msg := llm.Message{Role: it.Role, Content: it.Content, ToolCallID: it.ToolCallID}
		if len(it.ToolCalls) > 0 {
			msg.ToolCalls = make([]llm.ToolCall, len(it.ToolCalls))
			for i, c := range it.ToolCalls {
				msg.ToolCalls[i] = llm.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
			}
		}
		// Round-trip the summary marker so that on a runner re-attach
		// (second history frame) Snapshot still anchors on the latest
		// compact point. Event-kind items keep their raw payload as
		// content; we leave Kind empty for them because the LLM has
		// already seen that shape and the window logic does not care.
		if it.Kind == llm.KindSummary {
			msg.Kind = llm.KindSummary
		}
		out = append(out, msg)
	}
	return out
}

// maybeNudgeCompact injects a one-shot reminder telling the LLM to call
// compact_session at its next safe boundary, once the input-token
// usage of the previous turn crossed CompactTokenThreshold. The
// reminder is appended as a user-role message so it lands in the
// LLM's view on the upcoming round; we don't try to truncate, summarise
// for the LLM, or stop the in-flight task — the model is the only thing
// that knows when a clean cutover point is reached.
//
// Re-arms after the LLM actually compacts (cleared in the
// pendingSummary branch of the dispatch loop). Disabled when
// compactTokenThreshold is zero.
func (l *Loop) maybeNudgeCompact(cctx *Context) {
	if l.compactTokenThreshold <= 0 || l.compactNudged {
		return
	}
	if l.lastInputTokens < l.compactTokenThreshold {
		return
	}
	cctx.AppendUser(fmt.Sprintf(
		"<system_reminder>Context usage has reached %d input tokens (threshold %d). When you can stop at a clean step — typically after a tool result settles and before starting the next sub-task — call the compact_session tool with a thorough summary so subsequent turns can keep working without hitting the upstream's context window. Continue the current step if you're mid-flight; do NOT abandon work to compact immediately.</system_reminder>",
		l.lastInputTokens, l.compactTokenThreshold,
	))
	l.compactNudged = true
}

// dispatchCompactSession is the loop-side handler for the compact_session
// tool call. The tool itself is schema-only; we parse the args here and
// surface the trimmed summary back to the caller, which is responsible
// for placing the AppendSummary marker after every tool result in the
// round has been emitted (see the pendingSummary commentary).
//
// Bad-args paths are converted into IsError CallResults rather than Go
// errors because the LLM is the audience: a structured tool result lets
// it self-correct (rewrite a non-empty summary, retry) on the next round
// without us having to thread a separate error channel through the loop.
func dispatchCompactSession(args json.RawMessage) (string, tools.CallResult) {
	summary, err := local.ParseCompactSessionArgs(args)
	if err != nil {
		errBody, _ := json.Marshal(map[string]any{"error": err.Error()})
		return "", tools.CallResult{
			Source:     tools.SourceLocal,
			ResultJSON: errBody,
			IsError:    true,
			ErrMsg:     err.Error(),
		}
	}
	okBody, _ := json.Marshal(map[string]any{
		"ok":        true,
		"compacted": true,
		"note":      "Prior conversation has been replaced with your summary. Subsequent turns will see {system prompt} + this summary + anything new — proceed with the task using only what your summary preserved.",
	})
	return summary, tools.CallResult{Source: tools.SourceLocal, ResultJSON: okBody}
}

// hasSleepCall reports whether any tool call in the slice is a sleep call.
func hasSleepCall(calls []llm.ToolCall) bool {
	for _, c := range calls {
		if c.Name == local.SleepToolName {
			return true
		}
	}
	return false
}

func newTurnID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "turn_" + hex.EncodeToString(b[:])
}
