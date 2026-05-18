// Package runtime is the agent's main loop and the in-memory message
// context it manages between LLM calls. The runner is the durable
// store; runtime keeps a working copy that mirrors what the LLM should
// see on the next request.
package runtime

import "github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"

// Context is the rolling message list the runtime hands to the LLM each
// turn. Append* methods are the only mutators so the windowing policy
// lives in one place. Goroutine-unsafe — the loop is single-threaded by
// design; if multiple turns ever overlap we'll need to re-think the
// audit ordering before we re-think this lock.
//
// Windowing: the full message slice is the audit-grade in-memory record
// and grows unbounded; Snapshot — what we hand to the LLM — instead
// returns the slice from the most recent KindSummary entry onward. The
// compact_session tool is what appends those summary markers; until one
// has been written we just send the whole history. This replaces the
// older tail-window trim that could orphan a `tool` message from its
// matching `assistant(tool_calls=…)` mid-window and trip upstream's
// "tool message without preceding tool_calls" validator.
type Context struct {
	systemPrompt string
	messages     []llm.Message
}

func NewContext(systemPrompt string, history []llm.Message) *Context {
	return &Context{
		systemPrompt: systemPrompt,
		messages:     append([]llm.Message{}, history...),
	}
}

func (c *Context) SystemPrompt() string { return c.systemPrompt }

// Len reports how many messages are currently held. Used by the loop to
// detect whether a new user-side message was folded in mid-round, in
// which case the LLM needs another pass before the turn can finish.
func (c *Context) Len() int { return len(c.messages) }

// Snapshot returns a copy of the messages currently visible to the LLM.
// The slice starts at the most recent KindSummary entry — older history
// stays on the Context for audit but is no longer sent. When there is no
// summary marker the whole history is returned. Callers must not mutate.
func (c *Context) Snapshot() []llm.Message {
	start := 0
	for i := len(c.messages) - 1; i >= 0; i-- {
		if c.messages[i].Kind == llm.KindSummary {
			start = i
			break
		}
	}
	out := make([]llm.Message, len(c.messages)-start)
	copy(out, c.messages[start:])
	return out
}

func (c *Context) AppendUser(content string) {
	c.messages = append(c.messages, llm.Message{Role: "user", Content: content})
}

func (c *Context) AppendAssistant(content string, calls []llm.ToolCall) {
	c.messages = append(c.messages, llm.Message{
		Role:      "assistant",
		Content:   content,
		ToolCalls: calls,
	})
}

// AppendAssistantWithReasoning is the thinking-model variant: the
// upstream emitted a `reasoning` output item alongside the assistant
// text + tool calls. Providers like DeepSeek-Reasoner reject the next
// turn if the prior reasoning_content is missing from the history, so
// we attach it verbatim to the assistant Message and ToInputItems
// echoes it back on the next request.
func (c *Context) AppendAssistantWithReasoning(content, reasoning, signature string, calls []llm.ToolCall) {
	c.messages = append(c.messages, llm.Message{
		Role:               "assistant",
		Content:            content,
		Reasoning:          reasoning,
		ReasoningSignature: signature,
		ToolCalls:          calls,
	})
}

func (c *Context) AppendToolResult(callID, content string) {
	c.messages = append(c.messages, llm.Message{
		Role:       "tool",
		ToolCallID: callID,
		Content:    content,
	})
}

// AppendSummary records a compact-session checkpoint. Snapshot anchors
// on the most recent one, so on the next LLM call only the summary +
// anything appended after it is sent. Older messages stay in the slice
// for audit. The caller (Loop) places this AFTER all tool results in a
// round so the new window can never start with an orphan tool message.
func (c *Context) AppendSummary(content string) {
	c.messages = append(c.messages, llm.Message{
		Role:    "user",
		Kind:    llm.KindSummary,
		Content: content,
	})
}
