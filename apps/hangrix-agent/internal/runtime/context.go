// Package runtime is the agent's main loop and the in-memory message
// context it manages between LLM calls. The runner is the durable
// store; runtime keeps a working copy that mirrors what the LLM should
// see on the next request.
package runtime

import "github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"

// Context is the rolling message list the runtime hands to the LLM each
// turn. Append* methods are the only mutators so the trim policy lives
// in one place. Goroutine-unsafe — the loop is single-threaded by
// design; if multiple turns ever overlap we'll need to re-think the
// audit ordering before we re-think this lock.
type Context struct {
	systemPrompt string
	messages     []llm.Message

	// trimMaxMessages caps the number of messages sent to the LLM. Older
	// messages are dropped (not summarised — that's an M9 job). Set to 0
	// to disable.
	trimMaxMessages int
}

func NewContext(systemPrompt string, history []llm.Message) *Context {
	return &Context{
		systemPrompt:    systemPrompt,
		messages:        append([]llm.Message{}, history...),
		trimMaxMessages: 60,
	}
}

func (c *Context) SystemPrompt() string { return c.systemPrompt }

// Snapshot returns a copy of the messages currently visible to the LLM,
// after applying the window trim. Callers must not mutate.
func (c *Context) Snapshot() []llm.Message {
	if c.trimMaxMessages <= 0 || len(c.messages) <= c.trimMaxMessages {
		out := make([]llm.Message, len(c.messages))
		copy(out, c.messages)
		return out
	}
	// Tail-window trim: keep the last N messages. Simple and predictable;
	// summary-based trimming is in M9. The first message in a new context
	// is often a triggering event — losing it can be expensive — but the
	// LLM also has the system prompt and the immediately-preceding tool
	// turn, so we accept the cost in v1.
	tail := c.messages[len(c.messages)-c.trimMaxMessages:]
	out := make([]llm.Message, len(tail))
	copy(out, tail)
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
