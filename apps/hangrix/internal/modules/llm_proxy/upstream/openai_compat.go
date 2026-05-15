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

// OpenAICompat talks to vendors that speak OpenAI's older Chat
// Completions wire (POST /v1/chat/completions) — DeepSeek, OpenRouter,
// vLLM, Together, Groq, Mistral, …. Most "OpenAI-compatible" providers
// stopped at Chat Completions and never shipped Responses API, so this
// adapter is the one that actually does the translation work for
// downstream consumers using the modern Responses-API surface.
//
// Reasoning models exposed through this surface (DeepSeek-Reasoner,
// Mistral reasoning tier, …) emit a `reasoning_content` field beside
// `content` on assistant messages. We pull it onto Response.Reasoning
// (and surface as a `reasoning` output item by the wire layer); on the
// next turn the handler re-serialises history and KindReasoning items
// land back on the corresponding assistant message's reasoning_content
// field. That round-trip is what keeps DeepSeek's chain-of-thought
// context alive across turns.
type OpenAICompat struct{}

func NewOpenAICompat() *OpenAICompat { return &OpenAICompat{} }

func (*OpenAICompat) Type() domain.ProviderType { return domain.ProviderTypeOpenAICompat }

func (*OpenAICompat) Respond(ctx context.Context, req *Request) (*Response, error) {
	base := strings.TrimRight(req.BaseURL, "/")
	if base == "" {
		return nil, ErrBaseURLRequired
	}
	body, err := json.Marshal(buildChatCompletionsBody(req))
	if err != nil {
		return nil, fmt.Errorf("encode chat-completions request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)

	resp, err := req.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call chat-completions: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, &UpstreamError{StatusCode: resp.StatusCode, Message: snippet(raw), Raw: raw}
	}
	return parseChatCompletionsBody(raw, resp.StatusCode)
}

// chatRequest / chatMessage / chatTool* are the typed shapes the
// /v1/chat/completions wire expects. Content is a *string (not plain
// string + omitempty) because Chat Completions wants `"content": null`
// explicitly on an assistant message that has only tool_calls — an
// omitted field is rejected by DeepSeek among others, and an empty
// string is rejected by OpenAI for the same case. Pointer + no
// omitempty lets us emit null, plain text, or an empty body depending
// on what was set.
type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	Tools           []chatTool    `json:"tools,omitempty"`
	ToolChoice      string        `json:"tool_choice,omitempty"`
	MaxTokens       int           `json:"max_tokens,omitempty"`
	Temperature     *float64      `json:"temperature,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
	Stream          bool          `json:"stream"`
}

type chatMessage struct {
	Role               string         `json:"role"`
	Content            *string        `json:"content"` // nil → null, "" → "" — NO omitempty
	ReasoningContent   string         `json:"reasoning_content,omitempty"`
	ReasoningSignature string         `json:"reasoning_signature,omitempty"`
	ToolCalls          []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID         string         `json:"tool_call_id,omitempty"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function chatToolCallFunction `json:"function"`
}

type chatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// buildChatCompletionsBody emits a `/v1/chat/completions` request from
// a typed Request. The bulk of the work is folding the flat InputItem
// array into the nested-message shape Chat Completions expects:
//
//	flat:   [user, reasoning, assistant text, function_call, function_call_output, user, ...]
//	nested: [user, {assistant content + reasoning_content + tool_calls}, tool, user, ...]
//
// Specifically: a contiguous run of (reasoning?, assistant message?,
// function_call*) items collapses into ONE assistant chat message
// carrying content + reasoning_content + tool_calls. function_call_output
// items map to standalone tool-role messages.
func buildChatCompletionsBody(req *Request) chatRequest {
	body := chatRequest{
		Model:           req.Model,
		Messages:        inputItemsToChatMessages(req.Input, req.Instructions),
		MaxTokens:       req.MaxOutputTokens,
		Temperature:     req.Temperature,
		ReasoningEffort: req.ReasoningEffort, // some compat vendors honour it; the rest ignore unknown fields
		Stream:          false,
	}
	if len(req.Tools) > 0 {
		body.Tools = make([]chatTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			body.Tools = append(body.Tools, chatTool{
				Type: "function",
				Function: chatToolFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			})
		}
		body.ToolChoice = "auto"
	}
	return body
}

// pendingAssistant accumulates an assistant chat message under
// construction. It exists because Chat Completions wants assistant
// content + reasoning + tool_calls collapsed into one record, while
// Responses-API's input array emits them as siblings.
type pendingAssistant struct {
	active             bool
	content            string
	reasoning          string
	reasoningSignature string
	toolCalls          []chatToolCall
}

func (p *pendingAssistant) flush() (chatMessage, bool) {
	if !p.active {
		return chatMessage{}, false
	}
	m := chatMessage{
		Role:               "assistant",
		ReasoningContent:   p.reasoning,
		ReasoningSignature: p.reasoningSignature,
		ToolCalls:          p.toolCalls,
	}
	// Chat Completions semantics: when an assistant message has only
	// tool_calls (no text), `content` should be null, not "". Some
	// servers (DeepSeek included) reject "" alongside tool_calls.
	if p.content != "" || len(p.toolCalls) == 0 {
		c := p.content
		m.Content = &c
	}
	*p = pendingAssistant{}
	return m, true
}

func inputItemsToChatMessages(items []InputItem, instructions string) []chatMessage {
	out := []chatMessage{}
	if instructions != "" {
		s := instructions
		out = append(out, chatMessage{Role: "system", Content: &s})
	}
	var pending pendingAssistant
	flushPending := func() {
		if m, ok := pending.flush(); ok {
			out = append(out, m)
		}
	}

	for _, it := range items {
		switch it.Kind {
		case KindMessage:
			role := it.Role
			if role == "" {
				role = "user"
			}
			if role == "assistant" {
				// Merge consecutive assistant text into the pending
				// assistant message — Chat Completions has no concept
				// of two assistant messages in a row.
				if pending.active {
					pending.content += it.Text
				} else {
					pending.active = true
					pending.content = it.Text
				}
			} else {
				flushPending()
				s := it.Text
				out = append(out, chatMessage{Role: role, Content: &s})
			}
		case KindReasoning:
			if !pending.active {
				pending.active = true
			}
			pending.reasoning += it.Reasoning
			if it.ReasoningSignature != "" {
				pending.reasoningSignature = it.ReasoningSignature
			}
		case KindToolCall:
			if !pending.active {
				pending.active = true
			}
			pending.toolCalls = append(pending.toolCalls, chatToolCall{
				ID:   it.ToolCallID,
				Type: "function",
				Function: chatToolCallFunction{
					Name:      it.ToolName,
					Arguments: it.ToolArgs,
				},
			})
		case KindToolResult:
			flushPending()
			s := it.ToolResult
			out = append(out, chatMessage{
				Role:       "tool",
				ToolCallID: it.ToolCallID,
				Content:    &s,
			})
		}
	}
	flushPending()
	return out
}

// parseChatCompletionsBody decodes an upstream chat-completions response
// into a typed Response. We only read choices[0] — Chat Completions can
// emit multiple choices but no caller of our proxy sends `n>1`.
func parseChatCompletionsBody(raw []byte, statusCode int) (*Response, error) {
	var wire struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Role               string `json:"role"`
				Content            string `json:"content"`
				ReasoningContent   string `json:"reasoning_content"`
				ReasoningSignature string `json:"reasoning_signature"`
				ToolCalls          []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name string `json:"name"`
						Args string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt            int `json:"prompt_tokens"`
			Completion        int `json:"completion_tokens"`
			Total             int `json:"total_tokens"`
			CompletionDetails struct {
				Reasoning int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("decode chat-completions response: %w (body=%s)", err, snippet(raw))
	}
	out := &Response{ID: wire.ID, Raw: raw, StatusCode: statusCode}
	out.Usage = Usage{
		PromptTokens:     int32(wire.Usage.Prompt),
		CompletionTokens: int32(wire.Usage.Completion),
		ReasoningTokens:  int32(wire.Usage.CompletionDetails.Reasoning),
		TotalTokens:      int32(wire.Usage.Total),
	}
	if out.Usage.TotalTokens == 0 {
		out.Usage.TotalTokens = out.Usage.PromptTokens + out.Usage.CompletionTokens
	}
	if len(wire.Choices) > 0 {
		c := wire.Choices[0]
		out.Text = c.Message.Content
		out.Reasoning = c.Message.ReasoningContent
		out.ReasoningSignature = c.Message.ReasoningSignature
		for _, tc := range c.Message.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Args,
			})
		}
	}
	return out, nil
}
