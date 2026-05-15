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
// → thinking.budget_tokens.
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
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

// anthropicContentBlock covers every block type Anthropic accepts on
// the request side today: text, thinking (with optional signature),
// and — in the future — tool_use / tool_result. Per-kind fields use
// omitempty so a text block doesn't carry an empty thinking string and
// vice versa.
type anthropicContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
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

// inputItemsToAnthropicMessages folds the flat InputItem array into the
// nested-content-block shape Anthropic expects. Same merging pattern as
// the chat-completions translator: a contiguous (reasoning?, assistant
// message?) run collapses into one assistant message with content
// blocks in [thinking?, text?] order.
//
// Tool calls are NOT mapped today — Anthropic's tool_use / tool_result
// blocks have a slightly different shape and no current caller of this
// proxy exercises the Anthropic-tools path. Tool-call items are dropped
// silently when this adapter is invoked; the agent should not yet
// schedule sessions against anthropic-type providers if it needs tools.
func inputItemsToAnthropicMessages(items []InputItem) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(items))

	var pending struct {
		active            bool
		text              string
		thinking          string
		thinkingSignature string
	}
	flush := func() {
		if !pending.active {
			return
		}
		blocks := []anthropicContentBlock{}
		if pending.thinking != "" {
			blocks = append(blocks, anthropicContentBlock{
				Type:      "thinking",
				Thinking:  pending.thinking,
				Signature: pending.thinkingSignature,
			})
		}
		if pending.text != "" {
			blocks = append(blocks, anthropicContentBlock{Type: "text", Text: pending.text})
		}
		if len(blocks) > 0 {
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
		}
		pending.active = false
		pending.text = ""
		pending.thinking = ""
		pending.thinkingSignature = ""
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
				if pending.active {
					pending.text += it.Text
				} else {
					pending.active = true
					pending.text = it.Text
				}
				continue
			}
			flush()
			out = append(out, anthropicMessage{
				Role:    role,
				Content: []anthropicContentBlock{{Type: "text", Text: it.Text}},
			})
		case KindReasoning:
			if !pending.active {
				pending.active = true
			}
			pending.thinking += it.Reasoning
			if it.ReasoningSignature != "" {
				pending.thinkingSignature = it.ReasoningSignature
			}
		case KindToolCall, KindToolResult:
			// Not in scope for the M6a anthropic adapter; the
			// in-package documentation calls this out. Drop silently.
		}
	}
	flush()
	return out
}

// parseAnthropicBody decodes an Anthropic Messages response into a
// typed Response. content blocks split between Text (concatenated
// `text` blocks) and Reasoning (concatenated `thinking` blocks).
// Signed thinking captures the signature so the next turn can echo it
// back to satisfy Anthropic's strict-mode verification.
func parseAnthropicBody(raw []byte, statusCode int) (*Response, error) {
	var wire struct {
		ID      string `json:"id"`
		Content []struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
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
