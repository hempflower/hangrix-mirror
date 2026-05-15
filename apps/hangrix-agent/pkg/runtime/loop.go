package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/ipc"
	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/tools"
)

// Loop owns the message-pump that ties IPC, LLM and tools together.
// It reads inbound frames from `in`, drives the LLM, dispatches tool
// calls, and emits outbound frames on `out`. Single-instance; not safe
// to call Run concurrently with itself.
type Loop struct {
	in       *ipc.Reader
	out      *ipc.Writer
	llm      *llm.Client
	model    string
	registry *tools.Registry
	system   string

	// maxToolRounds caps how many LLM⇄tool round-trips we allow within
	// one inbound event. The cap is a fail-safe against an LLM that
	// churns indefinitely (e.g. calls a misnamed tool over and over).
	// Hit the cap → emit a log + done, let the runner decide what to do.
	maxToolRounds int
}

func NewLoop(
	in *ipc.Reader,
	out *ipc.Writer,
	llmClient *llm.Client,
	model string,
	registry *tools.Registry,
	systemPrompt string,
) *Loop {
	return &Loop{
		in:            in,
		out:           out,
		llm:           llmClient,
		model:         model,
		registry:      registry,
		system:        systemPrompt,
		maxToolRounds: 16,
	}
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

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		frame, err := l.in.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Runner closed stdin: graceful shutdown is implicit.
				return nil
			}
			return fmt.Errorf("runtime: read inbound: %w", err)
		}
		switch frame.Kind {
		case "control":
			if frame.Op == "shutdown" {
				return nil
			}
			_ = l.out.Log("warn", fmt.Sprintf("unknown control op: %s", frame.Op))
		case "event":
			if err := l.handleEvent(ctx, cctx, frame); err != nil {
				return err
			}
		case "history":
			// A second history frame is a re-sync (runner re-attaching
			// after agent restart). Replace the working context with the
			// runner's authoritative copy.
			cctx = NewContext(l.system, historyToMessages(frame.Messages))
			_ = l.out.Status("ready")
		default:
			_ = l.out.Log("warn", fmt.Sprintf("unknown inbound kind: %s", frame.Kind))
		}
	}
}

func (l *Loop) handleEvent(ctx context.Context, cctx *Context, frame *ipc.Inbound) error {
	turnID := newTurnID()
	// Render the event as a user-role message: a JSON wrapper that names
	// the event and includes the payload verbatim. The LLM sees a
	// well-formed structured trigger rather than free-form prose.
	eventMsg, err := renderEventMessage(frame.Event, frame.Payload)
	if err != nil {
		_ = l.out.Log("error", fmt.Sprintf("render event: %s", err))
		return nil
	}
	cctx.AppendUser(eventMsg)

	for round := 0; round < l.maxToolRounds; round++ {
		_ = l.out.Status("thinking")
		req := &llm.CreateRequest{
			Model:        l.model,
			Instructions: cctx.SystemPrompt(),
			Messages:     cctx.Snapshot(),
			Tools:        l.registry.Catalog(),
		}
		resp, err := l.llm.Create(ctx, req)
		if err != nil {
			_ = l.out.Log("error", fmt.Sprintf("llm: %s", err))
			_ = l.out.Done(turnID)
			return nil
		}
		cctx.AppendAssistant(resp.Content, resp.ToolCalls)
		_ = l.out.Message("assistant", resp.Content, toIPCToolCalls(resp.ToolCalls))

		if len(resp.ToolCalls) == 0 {
			_ = l.out.Done(turnID)
			return nil
		}

		for _, call := range resp.ToolCalls {
			_ = l.out.Status("tool")
			args := json.RawMessage(call.Arguments)
			result := l.registry.Call(ctx, call.Name, args)
			// What we feed back to the LLM: the JSON result, or an error
			// envelope when the call failed. Keep this consistent — the
			// LLM does better with one shape than alternating.
			toolContent := toolPayload(result)
			cctx.AppendToolResult(call.ID, toolContent)
			_ = l.out.ToolCall(call.ID, call.Name, args, json.RawMessage(toolContent))
		}
		// Loop iterates: feed the tool outputs back to the LLM for the
		// next assistant turn. Exit when the assistant returns no calls.
	}

	_ = l.out.Log("warn", fmt.Sprintf("max tool rounds (%d) exhausted", l.maxToolRounds))
	_ = l.out.Done(turnID)
	return nil
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
