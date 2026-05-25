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

// defaultOpenAIBaseURL is the canonical OpenAI host. Used when a
// provider row of type `openai` has an empty BaseURL — operators
// rarely override it, but the column is kept settable for staging
// fixtures and tunnels.
const defaultOpenAIBaseURL = "https://api.openai.com"

// OpenAI talks to upstreams that natively speak the OpenAI Responses
// API (POST /v1/responses). It's the only adapter that doesn't have to
// translate — Request maps almost 1:1 onto the Responses-API body.
//
// The adapter is intentionally a structured client (not a byte
// forwarder) so that input items, tool calls, and reasoning items get
// shaped consistently regardless of which JSON dialect quirks the
// caller sent.
type OpenAI struct{}

func NewOpenAI() *OpenAI { return &OpenAI{} }

func (*OpenAI) Type() domain.ProviderType { return domain.ProviderTypeOpenAI }

func (*OpenAI) Respond(ctx context.Context, req *Request) (*Response, error) {
	base := strings.TrimRight(req.BaseURL, "/")
	if base == "" {
		base = defaultOpenAIBaseURL
	}
	body, err := json.Marshal(buildResponsesAPIRequestBody(req))
	if err != nil {
		return nil, fmt.Errorf("encode openai request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)

	resp, err := req.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call openai: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, &UpstreamError{StatusCode: resp.StatusCode, Message: snippet(raw), Raw: raw}
	}
	return parseResponsesAPIResponseBody(raw, resp.StatusCode)
}

// openaiRequest is the typed shape of the body we send upstream to
// /v1/responses. Mirrors ParseResponsesAPIRequest's input wire, just
// in the outgoing direction.
type openaiRequest struct {
	Model           string            `json:"model"`
	Instructions    string            `json:"instructions,omitempty"`
	Input           []openaiInputItem `json:"input"`
	Tools           []openaiTool      `json:"tools,omitempty"`
	ToolChoice      string            `json:"tool_choice,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	Reasoning       *openaiReasoning  `json:"reasoning,omitempty"`
	Stream          bool              `json:"stream"`
}

// openaiInputItem covers every input-item kind in one record. Type is
// the discriminator; per-kind fields use omitempty so an item with
// e.g. only a function_call payload doesn't carry empty content blocks
// out on the wire.
type openaiInputItem struct {
	Type             string               `json:"type"`
	Role             string               `json:"role,omitempty"`
	Content          []openaiInputContent `json:"content,omitempty"`
	Summary          []responsesSummary   `json:"summary,omitempty"`
	EncryptedContent string               `json:"encrypted_content,omitempty"`
	CallID           string               `json:"call_id,omitempty"`
	Name             string               `json:"name,omitempty"`
	Arguments        string               `json:"arguments,omitempty"`
	Output           string               `json:"output,omitempty"`
}

type openaiInputContent struct {
	Type string `json:"type"` // "input_text" (user/system) | "output_text" (assistant)
	Text string `json:"text"`
}

type openaiTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openaiReasoning struct {
	Effort string `json:"effort"`
}

// buildResponsesAPIRequestBody is the inverse of
// ParseResponsesAPIRequest at one extra hop: the handler parses
// inbound, the adapter re-emits to a Responses-API upstream. Same
// shape, just typed for marshaling instead of unmarshaling.
func buildResponsesAPIRequestBody(req *Request) openaiRequest {
	body := openaiRequest{
		Model:           req.Model,
		Instructions:    req.Instructions,
		Input:           inputItemsToOpenAI(req.Input),
		MaxOutputTokens: req.MaxOutputTokens,
		Temperature:     req.Temperature,
		Stream:          false,
	}
	if len(req.Tools) > 0 {
		body.Tools = make([]openaiTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			body.Tools = append(body.Tools, openaiTool{
				Type:        "function",
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
		body.ToolChoice = "auto"
	}
	if req.ReasoningEffort != "" {
		body.Reasoning = &openaiReasoning{Effort: req.ReasoningEffort}
	}
	return body
}

func inputItemsToOpenAI(items []InputItem) []openaiInputItem {
	out := make([]openaiInputItem, 0, len(items))
	for _, it := range items {
		switch it.Kind {
		case KindMessage:
			role := it.Role
			if role == "" {
				role = "user"
			}
			// Assistant turns carry output_text blocks; everyone else
			// uses input_text. Mirrors what the Responses API emits
			// for its own history.
			blockType := "input_text"
			if role == "assistant" {
				blockType = "output_text"
			}
			out = append(out, openaiInputItem{
				Type:    "message",
				Role:    role,
				Content: []openaiInputContent{{Type: blockType, Text: it.Text}},
			})
		case KindReasoning:
			out = append(out, openaiInputItem{
				Type:             "reasoning",
				Summary:          []responsesSummary{{Type: "summary_text", Text: it.Reasoning}},
				EncryptedContent: it.ReasoningSignature,
			})
		case KindToolCall:
			out = append(out, openaiInputItem{
				Type:      "function_call",
				CallID:    it.ToolCallID,
				Name:      it.ToolName,
				Arguments: it.ToolArgs,
			})
		case KindToolResult:
			out = append(out, openaiInputItem{
				Type:   "function_call_output",
				CallID: it.ToolCallID,
				Output: it.ToolResult,
			})
		}
	}
	return out
}

// parseResponsesAPIResponseBody decodes an upstream Responses-API
// response into a typed Response. Handles the three output kinds
// (message / reasoning / function_call) and surfaces reasoning_tokens
// when the upstream emits the o-series usage breakdown.
func parseResponsesAPIResponseBody(raw []byte, statusCode int) (*Response, error) {
	var wire struct {
		ID     string `json:"id"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			CallID  string `json:"call_id"`
			Name    string `json:"name"`
			Args    string `json:"arguments"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Summary []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"summary"`
			EncryptedContent string `json:"encrypted_content"`
		} `json:"output"`
		Usage struct {
			Input         int `json:"input_tokens"`
			Output        int `json:"output_tokens"`
			Total         int `json:"total_tokens"`
			OutputDetails struct {
				Reasoning int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("decode openai response: %w (body=%s)", err, snippet(raw))
	}
	out := &Response{ID: wire.ID, Raw: raw, StatusCode: statusCode}
	out.Usage = Usage{
		PromptTokens:     int32(wire.Usage.Input),
		CompletionTokens: int32(wire.Usage.Output),
		ReasoningTokens:  int32(wire.Usage.OutputDetails.Reasoning),
		TotalTokens:      int32(wire.Usage.Total),
	}
	if out.Usage.TotalTokens == 0 {
		out.Usage.TotalTokens = out.Usage.PromptTokens + out.Usage.CompletionTokens
	}
	var textBuf, reasoningBuf strings.Builder
	for _, item := range wire.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" {
					textBuf.WriteString(c.Text)
				}
			}
		case "reasoning":
			for _, s := range item.Summary {
				if s.Type == "" || s.Type == "summary_text" {
					reasoningBuf.WriteString(s.Text)
				}
			}
			if item.EncryptedContent != "" {
				out.ReasoningSignature = item.EncryptedContent
			}
		case "function_call":
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: item.Args,
			})
		}
	}
	out.Text = textBuf.String()
	out.Reasoning = reasoningBuf.String()
	return out, nil
}

// snippet caps an upstream body for use in an error message so a
// 1 MB upstream HTML page can't drown the proxy log.
func snippet(b []byte) string {
	const cap = 512
	if len(b) <= cap {
		return string(b)
	}
	return string(b[:cap]) + "…"
}
