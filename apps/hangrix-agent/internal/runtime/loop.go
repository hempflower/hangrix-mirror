package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/ipc"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
)

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
	bash     local.BashLifecycle

	// maxToolRounds caps how many LLM⇄tool round-trips we allow within
	// one inbound event. The cap exists only as a runaway-loop fail-safe;
	// in practice an agent should never approach it. Lifted to a very
	// large number so legitimate long sessions (refactors that touch
	// many files, multi-step debugging) don't get cut off mid-stream.
	maxToolRounds int

	// shutdownGrace bounds how long Cleanup waits for background bash
	// tasks to ack SIGKILL on shutdown before we exit anyway. The
	// container teardown will reap whatever is left; this just keeps
	// the exit path from wedging on a pathological child.
	shutdownGrace time.Duration
}

func NewLoop(
	in *ipc.Reader,
	out *ipc.Writer,
	llmClient *llm.Client,
	model string,
	registry *tools.Registry,
	systemPrompt string,
	bash local.BashLifecycle,
) *Loop {
	return &Loop{
		in:            in,
		out:           out,
		llm:           llmClient,
		model:         model,
		registry:      registry,
		system:        systemPrompt,
		bash:          bash,
		maxToolRounds: 999999,
		shutdownGrace: 5 * time.Second,
	}
}

// inboxItem is one thing the loop reacts to. Exactly one of Frame /
// Notification is populated per item. The reader goroutine (stdin) and
// the bashTool notification goroutine both push into the same channel —
// the loop never has to multiplex sources itself.
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
	if l.bash == nil {
		return
	}
	if l.bash.HasRunningJobs() == 0 {
		return
	}
	gctx, cancel := context.WithTimeout(context.Background(), l.shutdownGrace)
	defer cancel()
	l.bash.Cleanup(gctx)
}

// emitIdle reports event-done state to the runner along with a hint
// about how many background bash tasks are still alive. The runner uses
// running_jobs to pick the right idle timeout (don't retire a container
// that's babysitting a long test run).
func (l *Loop) emitIdle() error {
	n := 0
	if l.bash != nil {
		n = l.bash.HasRunningJobs()
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
	for round := 0; round < l.maxToolRounds; round++ {
		// Drain anything the inbox accumulated since the last round
		// boundary. Non-blocking — we only consume what's already
		// queued. Notifications go straight into the context;
		// non-event frames get deferred for the outer loop to handle
		// after this event is over.
		l.drainPending(cctx, inbox, pendingFrames)

		_ = l.out.Status("thinking")
		req := &llm.CreateRequest{
			Model:        l.model,
			Instructions: cctx.SystemPrompt(),
			Messages:     cctx.Snapshot(),
			Tools:        l.registry.Catalog(),
		}

		// Run the LLM call in its own goroutine so the main goroutine
		// can keep draining the inbox. A notification arriving mid-call
		// is appended to the context but does NOT cancel the call —
		// canceling would waste tokens, and the notification will be
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
				switch {
				case item.ReaderErr != nil:
					// stdin failure — defer it for the outer loop so
					// the EOF/error message lands in the same handler
					// as any other reader error.
					*pendingFrames = append(*pendingFrames, &ipc.Inbound{Kind: "__reader_err__"})
					_ = item // not used further; outer loop will surface
				case item.Frame != nil:
					// Defer non-event frames (notably control:shutdown)
					// to after the current event. Event frames stack
					// up behind the current one too — we never start
					// a second event mid-round.
					*pendingFrames = append(*pendingFrames, item.Frame)
				case item.Notification != "":
					// Background task finished mid-LLM-call. Append it
					// to the context so it's visible at the next round
					// — but don't cancel the in-flight call.
					cctx.AppendUser(item.Notification)
				}
			}
		}

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

		if len(resp.ToolCalls) == 0 {
			_ = l.out.Done(turnID)
			return nil
		}

		for _, call := range resp.ToolCalls {
			_ = l.out.Status("tool")
			args := json.RawMessage(call.Arguments)
			result := l.registry.Call(ctx, call.Name, args)
			toolContent := toolPayload(result)
			cctx.AppendToolResult(call.ID, toolContent)
			_ = l.out.ToolCall(call.ID, call.Name, args, json.RawMessage(toolContent))
		}
	}

	_ = l.out.Log("warn", fmt.Sprintf("max tool rounds (%d) exhausted", l.maxToolRounds))
	_ = l.out.Done(turnID)
	return nil
}

// drainPending consumes whatever is already buffered on the inbox
// without blocking, routing each item to its destination: notifications
// into the LLM context, frames into the deferred queue.
func (l *Loop) drainPending(cctx *Context, inbox <-chan inboxItem, pendingFrames *[]*ipc.Inbound) {
	for {
		select {
		case item, ok := <-inbox:
			if !ok {
				return
			}
			switch {
			case item.ReaderErr != nil:
				*pendingFrames = append(*pendingFrames, &ipc.Inbound{Kind: "__reader_err__"})
			case item.Frame != nil:
				*pendingFrames = append(*pendingFrames, item.Frame)
			case item.Notification != "":
				cctx.AppendUser(item.Notification)
			}
		default:
			return
		}
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

// pumpNotifications forwards bashTool completion notifications into the
// inbox. The channel from b.NotificationCh() is buffered on the bashTool
// side too; this goroutine just adapts it into the unified inbox shape
// so the loop doesn't have to select on N source-specific channels.
func (l *Loop) pumpNotifications(ctx context.Context, inbox chan<- inboxItem) {
	if l.bash == nil {
		return
	}
	ch := l.bash.NotificationCh()
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
	wrapper := map[string]any{
		"hangrix_event": event,
	}
	if len(payload) > 0 {
		var p any
		if err := json.Unmarshal(payload, &p); err != nil {
			return "", err
		}
		wrapper["payload"] = p
	}
	out, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
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
		// History items tagged kind="event" carry the raw event payload
		// as content; the LLM has already seen this shape, so just
		// forward it as-is.
		out = append(out, msg)
	}
	return out
}

func newTurnID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "turn_" + hex.EncodeToString(b[:])
}
