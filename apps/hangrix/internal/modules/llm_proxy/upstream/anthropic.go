package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
)

// defaultAnthropicBaseURL is the canonical Anthropic Messages host.
// Used when a provider row of type `anthropic` has an empty BaseURL.
const defaultAnthropicBaseURL = "https://api.anthropic.com"

// anthropicAPIVersion is the wire version we negotiate. Hard-coded
// because translating across versions is not in scope today; bumping
// it is a deliberate code change with a translator review.
const anthropicAPIVersion = "2023-06-01"

// defaultMaxTokens applies when the caller omits max_output_tokens.
// Anthropic requires max_tokens to be set; OpenAI's Responses API
// treats it as optional. 4096 is a safe ceiling for short interactive
// turns; thinking-enabled requests bump this in buildAnthropicBody.
const defaultMaxTokens = 4096

// Anthropic talks to upstreams that speak Anthropic's Messages API
// (POST /v1/messages). Translates a typed Request into Messages shape
// and back. Extended thinking is wired in via Request.ReasoningEffort
// → thinking.budget_tokens. Tool calling is fully bidirectional:
// Request.Tools become the `tools` array; prior KindToolCall /
// KindToolResult input items materialise as (assistant.tool_use,
// user.tool_result) content blocks; upstream tool_use blocks decode
// back into Response.ToolCalls so the rest of the agent pipeline sees
// the same shape as OpenAI-style providers.
type Anthropic struct{}

func NewAnthropic() *Anthropic { return &Anthropic{} }

func (*Anthropic) Type() domain.ProviderType { return domain.ProviderTypeAnthropic }

func (*Anthropic) Respond(ctx context.Context, req *Request) (*Response, error) {
	base := strings.TrimRight(req.BaseURL, "/")
	if base == "" {
		base = defaultAnthropicBaseURL
	}
	body, err := json.Marshal(buildAnthropicBody(req))
	if err != nil {
		return nil, fmt.Errorf("encode anthropic request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	// Anthropic uses x-api-key + anthropic-version; do NOT set
	// Authorization: Bearer (different scheme, would just be ignored
	// but could leak the key through their access logs).
	httpReq.Header.Set("x-api-key", req.APIKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := req.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call anthropic: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, &UpstreamError{StatusCode: resp.StatusCode, Message: anthropicErrorMessage(raw), Raw: raw}
	}
	return parseAnthropicBody(raw, resp.StatusCode)
}

// anthropicRequest / anthropicMessage / anthropicContentBlock are the
// typed shapes the /v1/messages wire expects. Thinking is optional
// (pointer) so an unrequested extended-thinking session doesn't carry
// an empty thinking object on the wire.
type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

// anthropicContentBlock covers every block type Anthropic accepts on
// the request and response side: text, thinking (with optional
// signature), tool_use (assistant's request to call a tool), and
// tool_result (caller's reply to a prior tool_use). Per-kind fields
// use omitempty so a text block doesn't carry empty tool_use fields
// and vice versa.
type anthropicContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`

	// tool_use blocks (assistant → caller). ID is the toolu_ handle
	// the matching tool_result must echo back. Input is the tool's
	// JSON-object argument — RawMessage so the agent's already-JSON
	// argument string round-trips as a nested object on the wire,
	// not as a re-quoted string.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result blocks (caller → assistant). ToolUseID points back
	// to the tool_use this is answering; Content is the textual
	// payload the tool produced.
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// anthropicTool mirrors the entry shape in the request's `tools`
// array. Note `input_schema` not `parameters` — Anthropic uses its
// own JSON-Schema field name.
type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// buildAnthropicBody emits an Anthropic Messages request body. Three
// translation points worth calling out:
//
//   - Instructions → `system` field (top-level, not a message).
//   - ReasoningEffort → `thinking.{type:enabled, budget_tokens:N}`;
//     when present, temperature is dropped and max_tokens is bumped
//     above budget_tokens (Anthropic rejects otherwise).
//   - InputItem array → flat Anthropic messages, with KindReasoning
//     items folded into the next assistant message as a `thinking`
//     content block so multi-turn conversations preserve prior
//     chain-of-thought.
func buildAnthropicBody(req *Request) anthropicRequest {
	maxTokens := defaultMaxTokens
	if req.MaxOutputTokens > 0 {
		maxTokens = req.MaxOutputTokens
	}
	body := anthropicRequest{
		Model:       req.Model,
		System:      req.Instructions,
		Messages:    inputItemsToAnthropicMessages(req.Input),
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
	}
	if budget := thinkingBudgetTokens(req.ReasoningEffort); budget > 0 {
		// Anthropic requires temperature unset (defaults to 1) when
		// thinking is enabled; drop the caller's value rather than
		// 400ing upstream.
		body.Temperature = nil
		body.Thinking = &anthropicThinking{
			Type:         "enabled",
			BudgetTokens: budget,
		}
		// max_tokens must exceed budget_tokens with headroom for the
		// final answer. Bump if the current ceiling is too tight.
		if minMax := budget + 4096; body.MaxTokens < minMax {
			body.MaxTokens = minMax
		}
	}
	if len(req.Tools) > 0 {
		body.Tools = make([]anthropicTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.Parameters
			if schema == nil {
				// Anthropic requires input_schema; fall back to the
				// permissive empty-object form so a poorly-described
				// upstream tool is still callable rather than 400-ing
				// the whole request.
				schema = map[string]any{"type": "object"}
			}
			body.Tools = append(body.Tools, anthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: schema,
			})
		}
	}
	return body
}

// thinkingBudgetTokens maps an OpenAI `reasoning.effort` enum to an
// Anthropic `thinking.budget_tokens` value. Anthropic requires
// budget_tokens ≥ 1024 and strictly less than max_tokens.
func thinkingBudgetTokens(effort string) int {
	switch effort {
	case "minimal", "low":
		return 1024
	case "medium":
		return 4096
	case "high":
		return 16384
	}
	return 0
}

// inputItemsToAnthropicMessages folds the flat InputItem array into
// the nested-content-block shape Anthropic expects.
//
// Two side-buckets run in parallel:
//
//   - pendingA: the assistant message under construction (zero or
//     more of: thinking, text, tool_use blocks, in spec-order). A
//     single assistant turn can contain many tool_use blocks
//     side-by-side with text, and Anthropic preserves the order.
//   - pendingU: the user message under construction. Accumulates
//     consecutive tool_result blocks (and optionally text) so a turn
//     that resolved N parallel tool_use calls becomes ONE user
//     message with N tool_result blocks — the protocol shape
//     Anthropic enforces.
//
// When the stream type switches direction (assistant → user or vice
// versa), the bucket on the other side flushes. A trailing
// flushA/flushU at the end emits anything left.
func inputItemsToAnthropicMessages(items []InputItem) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(items))

	var pendingA struct {
		active            bool
		text              string
		thinking          string
		thinkingSignature string
		toolUses          []anthropicContentBlock
	}
	flushA := func() {
		if !pendingA.active {
			return
		}
		blocks := []anthropicContentBlock{}
		if pendingA.thinking != "" {
			blocks = append(blocks, anthropicContentBlock{
				Type:      "thinking",
				Thinking:  pendingA.thinking,
				Signature: pendingA.thinkingSignature,
			})
		}
		if pendingA.text != "" {
			blocks = append(blocks, anthropicContentBlock{Type: "text", Text: pendingA.text})
		}
		blocks = append(blocks, pendingA.toolUses...)
		if len(blocks) > 0 {
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
		}
		pendingA.active = false
		pendingA.text = ""
		pendingA.thinking = ""
		pendingA.thinkingSignature = ""
		pendingA.toolUses = nil
	}

	var pendingU []anthropicContentBlock
	flushU := func() {
		if len(pendingU) == 0 {
			return
		}
		out = append(out, anthropicMessage{Role: "user", Content: pendingU})
		pendingU = nil
	}

	for _, it := range items {
		switch it.Kind {
		case KindMessage:
			role := it.Role
			if role == "" {
				role = "user"
			}
			if role == "system" {
				// System content rides on the top-level `system`
				// field; we drop inline system messages rather than
				// pretending they're user content.
				continue
			}
			if role == "assistant" {
				flushU()
				pendingA.active = true
				pendingA.text += it.Text
				continue
			}
			// role == "user" (or any unknown role we treat as user).
			flushA()
			pendingU = append(pendingU, anthropicContentBlock{Type: "text", Text: it.Text})
		case KindReasoning:
			flushU()
			pendingA.active = true
			pendingA.thinking += it.Reasoning
			if it.ReasoningSignature != "" {
				pendingA.thinkingSignature = it.ReasoningSignature
			}
		case KindToolCall:
			flushU()
			pendingA.active = true
			input := json.RawMessage(strings.TrimSpace(it.ToolArgs))
			if len(input) == 0 {
				// Anthropic requires input to be a JSON object even
				// when the tool takes no arguments. Empty-string from
				// the agent maps to `{}`.
				input = json.RawMessage("{}")
			}
			pendingA.toolUses = append(pendingA.toolUses, anthropicContentBlock{
				Type:  "tool_use",
				ID:    it.ToolCallID,
				Name:  it.ToolName,
				Input: input,
			})
		case KindToolResult:
			flushA()
			pendingU = append(pendingU, anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: it.ToolCallID,
				Content:   it.ToolResult,
			})
		}
	}
	flushA()
	flushU()
	return out
}

// parseAnthropicBody decodes an Anthropic Messages response into a
// typed Response. content blocks split between Text (concatenated
// `text` blocks), Reasoning (concatenated `thinking` blocks), and
// ToolCalls (one per `tool_use` block, in order). Signed thinking
// captures the signature so the next turn can echo it back to satisfy
// Anthropic's strict-mode verification.
//
// Tool-use blocks carry their `input` as a JSON object on the wire;
// we re-encode it back to a string so it matches the OpenAI-shaped
// `function.arguments` the rest of the proxy / agent path expects.
func parseAnthropicBody(raw []byte, statusCode int) (*Response, error) {
	var wire struct {
		ID      string `json:"id"`
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w (body=%s)", err, snippet(raw))
	}
	out := &Response{ID: wire.ID, Raw: raw, StatusCode: statusCode}
	out.Usage = Usage{
		PromptTokens:     int32(wire.Usage.Input),
		CompletionTokens: int32(wire.Usage.Output),
		TotalTokens:      int32(wire.Usage.Input + wire.Usage.Output),
	}
	var text, thinking strings.Builder
	for _, b := range wire.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "thinking":
			thinking.WriteString(b.Thinking)
			if b.Signature != "" {
				out.ReasoningSignature = b.Signature
			}
		case "tool_use":
			args := strings.TrimSpace(string(b.Input))
			if args == "" {
				args = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:        b.ID,
				Name:      b.Name,
				Arguments: args,
			})
		}
	}
	out.Text = text.String()
	out.Reasoning = thinking.String()
	return out, nil
}

// anthropicErrorMessage extracts the human-readable message out of an
// Anthropic error envelope so UpstreamError surfaces something
// actionable instead of "upstream 400: ...".
func anthropicErrorMessage(raw []byte) string {
	if len(raw) == 0 {
		return "anthropic upstream error"
	}
	var obj struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Error.Message != "" {
		return obj.Error.Message
	}
	return snippet(raw)
}
