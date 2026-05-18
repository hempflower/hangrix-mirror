// Package ipc implements the JSON-Lines protocol the agent speaks with its
// runner over stdin/stdout. The runner writes one JSON object per line on
// the agent's stdin (history replay, events, control); the agent writes one
// JSON object per line on stdout (status, message, tool_call, log, done,
// idle).
//
// The protocol is intentionally line-oriented and one-direction-per-fd so
// that under docker the runner can attach plain pipes — no length framing,
// no multiplexing, no stdin/stdout interleaving rules to memorise.
//
// Lifecycle note. The agent is long-lived: it processes events one after
// another over the lifetime of a single container, and emits an `idle`
// frame after each event finishes. The runner uses that as the signal
// that this container can either receive the next queued event or be
// retired via a `control:shutdown` inbound when its idle-timeout elapses.
// `done` still bounds individual events; `idle` bounds the container.
package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Inbound is one frame the runner sends to the agent. Exactly one of the
// optional fields is populated per Kind:
//
//   - "history": Messages holds the replayed session history (may be empty
//     for a brand new session).
//   - "event":   Event names the trigger; Payload carries the JSON body.
//   - "control": Op names the control command (today only "shutdown").
//
// We keep all fields on one struct rather than a tagged union because the
// JSON wire form already discriminates on Kind; an Unmarshal-into-Inbound
// is one allocation versus a two-step decode.
type Inbound struct {
	Kind     string          `json:"kind"`
	Messages []HistoryItem   `json:"messages,omitempty"`
	Event    string          `json:"event,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Op       string          `json:"op,omitempty"`
}

// HistoryItem is one replayed message from the runner. Mirrors the shape
// the agent emits on stdout (Outbound{Kind:"message"}|{Kind:"tool_call"})
// flattened back into a single sequence — runner is the persistence
// layer, agent rebuilds context from these.
type HistoryItem struct {
	Role       string     `json:"role"`              // "user" | "assistant" | "tool" | "system"
	Kind       string     `json:"kind,omitempty"`    // optional sub-tag, e.g. "event"
	Event      string     `json:"event,omitempty"`   // populated when Kind == "event"
	Content    string     `json:"content,omitempty"` // text body
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // populated when Role == "tool"
}

// ToolCall is the function-call shape both the runner and the LLM emit.
// Arguments is the raw JSON the LLM produced — we keep it as a string
// rather than re-decoding because the local/remote tool dispatcher will
// want to round-trip it as the spec for OpenAI function calling requires.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Outbound is one frame the agent sends to the runner. Same single-struct
// rationale as Inbound. The five Kind values map 1:1 to the five outbound
// shapes documented in the M6b spec.
type Outbound struct {
	Kind string `json:"kind"`

	// status
	Phase string `json:"phase,omitempty"`

	// message (assistant turn)
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// tool_call (post-execution; result included so runner can persist as one row)
	Name       string          `json:"name,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`

	// log
	Level string `json:"level,omitempty"`
	Msg   string `json:"msg,omitempty"`

	// done
	TurnID string `json:"turn_id,omitempty"`

	// idle (emitted after each event so the runner knows the container
	// is reusable for the next queued event). RunningJobs is the count
	// of background bash tasks still alive when idle is emitted, so the
	// runner can pick a more generous timeout (don't retire a container
	// that's babysitting a `go test` that hasn't returned yet).
	RunningJobs int `json:"running_jobs,omitempty"`
}

// Reader scans inbound frames off a buffered stdin. bufio.Scanner is fine
// for newline-delimited JSON if we lift the line cap — the runner can send
// a full session history in the first frame, which routinely exceeds the
// 64 KiB default. 16 MiB is chosen as the same order of magnitude as the
// proxy's 4 MiB body cap, doubled twice to leave headroom for retries
// stacking history items.
type Reader struct {
	sc *bufio.Scanner
}

const maxIPCLine = 16 << 20

func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxIPCLine)
	return &Reader{sc: sc}
}

// Read returns the next inbound frame. io.EOF surfaces unwrapped so
// callers can detect a closed runner pipe and shut down cleanly.
func (r *Reader) Read() (*Inbound, error) {
	if !r.sc.Scan() {
		if err := r.sc.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	line := r.sc.Bytes()
	if len(line) == 0 {
		// Empty lines are silently skipped — runners may emit them as a
		// keep-alive or after a chunked write. Recursing once is fine
		// because the underlying reader has already advanced past the
		// blank line.
		return r.Read()
	}
	var in Inbound
	if err := json.Unmarshal(line, &in); err != nil {
		return nil, fmt.Errorf("ipc: invalid inbound frame: %w", err)
	}
	if in.Kind == "" {
		return nil, errors.New("ipc: inbound frame missing kind")
	}
	return &in, nil
}

// Writer serialises outbound frames behind a mutex so concurrent senders
// (the runtime loop emits messages while goroutine-driven log lines may
// fire from tool execution) cannot interleave bytes on stdout.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// Write encodes one frame followed by a newline. Errors propagate to the
// caller; the runtime treats a write error on stdout as fatal because the
// runner is the only consumer.
func (w *Writer) Write(out *Outbound) error {
	buf, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("ipc: marshal outbound: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.w.Write(buf); err != nil {
		return err
	}
	_, err = w.w.Write([]byte{'\n'})
	return err
}

// Status / Log / Message / ToolCall / Done are the five canonical outbound
// shapes. They are thin wrappers around Write — kept as methods so the
// runtime call sites read like the spec's IPC table.
func (w *Writer) Status(phase string) error {
	return w.Write(&Outbound{Kind: "status", Phase: phase})
}

func (w *Writer) Log(level, msg string) error {
	return w.Write(&Outbound{Kind: "log", Level: level, Msg: msg})
}

func (w *Writer) Message(role, content string, calls []ToolCall) error {
	return w.Write(&Outbound{Kind: "message", Role: role, Content: content, ToolCalls: calls})
}

func (w *Writer) ToolCall(id, name string, args, result json.RawMessage) error {
	return w.Write(&Outbound{Kind: "tool_call", ToolCallID: id, Name: name, Args: args, Result: result})
}

func (w *Writer) Done(turnID string) error {
	return w.Write(&Outbound{Kind: "done", TurnID: turnID})
}

// Idle is emitted by the runtime after each event finishes — it signals
// to the runner that this container is reusable for the next queued
// event. runningJobs reports how many background bash tasks are still
// alive (a hint for the runner's idle-timeout policy; 0 means truly
// idle, retire whenever).
func (w *Writer) Idle(runningJobs int) error {
	return w.Write(&Outbound{Kind: "idle", RunningJobs: runningJobs})
}
