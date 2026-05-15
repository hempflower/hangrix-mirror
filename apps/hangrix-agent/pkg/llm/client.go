// Package llm is a hand-rolled OpenAI Response API client. It deliberately
// has no dependency on any third-party LLM SDK — Hangrix wants the wire
// shape, retry policy, and tool-call parsing all under our control because
// the agent loop integrates them with audit, role identity, and IPC in a
// way SDK abstractions tend to fight.
//
// Endpoint shape: POST {endpoint}/responses with JSON
//
//	{
//	  "model": "...",
//	  "input": [item, item, ...],
//	  "instructions": "system prompt",
//	  "tools": [{type: "function", name, description, parameters}],
//	  "tool_choice": "auto",
//	  "stream": false
//	}
//
// Response shape:
//
//	{
//	  "id": "resp_...",
//	  "output": [item, item, ...],
//	  "usage": { input_tokens, output_tokens, total_tokens }
//	}
//
// Where each item is one of:
//   - {"type":"message","role":"assistant","content":[{"type":"output_text","text":"..."}]}
//   - {"type":"function_call","call_id":"...","name":"...","arguments":"<json string>"}
//   - {"type":"function_call_output","call_id":"...","output":"..."}
//
// The agent's internal Message struct (role/content/tool_calls/tool_call_id)
// flattens that — ToInputItems converts back at request time.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Message is the agent's flat in-memory message shape. Mirrors the
// HistoryItem in pkg/ipc but stays inside the LLM package boundary so we
// could swap out the IPC layer (e.g. for a unit test) without touching the
// runtime context.
type Message struct {
	Role       string     // "system" | "user" | "assistant" | "tool"
	Content    string     // plain-text body
	ToolCalls  []ToolCall // populated on assistant messages with function calls
	ToolCallID string     // populated on tool messages, joins back to ToolCall.ID
}

// ToolCall mirrors the OpenAI function_call shape. Arguments stays as a
// raw JSON string because that is what upstream serialises and what the
// downstream tool dispatcher round-trips.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ToolDescriptor is the function-tool schema the LLM consumes. We accept
// the raw schema as map[string]any so callers can feed in JSON-Schema
// fragments without converting them through an intermediate Go type.
type ToolDescriptor struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON-Schema object
}

// CreateRequest is the agent-level call input. The client converts this
// into the wire-level Response API request inside Create.
type CreateRequest struct {
	Model        string
	Instructions string // system prompt block
	Messages     []Message
	Tools        []ToolDescriptor
	// MaxOutputTokens, when >0, caps the upstream's per-call output budget.
	// Zero means "let the upstream apply its default".
	MaxOutputTokens int
}

// CreateResponse is the parsed result. Content is the concatenated text of
// every output_text item in the assistant message; ToolCalls are the
// function_call items in the order they appeared.
type CreateResponse struct {
	ID         string
	Content    string
	ToolCalls  []ToolCall
	Usage      Usage
	StatusCode int    // upstream HTTP status (always 2xx on success)
	Raw        []byte // unparsed response body — kept for audit, never logged here
}

type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// Client speaks to one LLM endpoint with one bearer token. The agent
// constructs this once at startup from HANGRIX_LLM_ENDPOINT and
// HANGRIX_SESSION_TOKEN — those values cannot change mid-session, so the
// client itself is immutable.
type Client struct {
	endpoint  string
	token     string
	http      *http.Client
	userAgent string
}

// New builds a Client. endpoint must be the proxy mount (e.g.
// https://hangrix.example/api/llm/openai-prod/v1). The trailing slash is
// stripped here so callers don't have to be careful — concatenating
// endpoint+"/responses" must produce a valid URL.
func New(endpoint, token string) *Client {
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		// 5-minute non-stream cap matches the proxy's upstreamTimeout, so a
		// timeout we see here means the proxy timed out too — we won't be
		// "stealing" a still-running upstream request.
		http:      &http.Client{Timeout: 5 * time.Minute},
		userAgent: "hangrix-agent/0.1",
	}
}

// Create issues one non-streaming /responses call. Retries with
// exponential backoff on transport errors and 5xx; 4xx (except 429)
// returns immediately because retrying a malformed request just wastes
// quota. 429 is retried with the same backoff because the proxy may
// surface upstream rate-limit signals that way.
func (c *Client) Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error) {
	body, err := buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("llm: build request: %w", err)
	}

	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// 0.5s, 1s, 2s — capped, no jitter (single client per session
			// means concurrent retries from one Hangrix node aren't a
			// thundering-herd concern; jitter is for fleets, not a loop).
			delay := time.Duration(math.Pow(2, float64(attempt-1))*500) * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		resp, err := c.do(ctx, body)
		if err != nil {
			lastErr = err
			// Network errors are retryable; context cancellation is not.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		// 2xx: parse and return.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return parseResponse(resp)
		}
		// 5xx and 429: retry. Other 4xx: bail.
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastErr = fmt.Errorf("llm: upstream %d: %s", resp.StatusCode, truncateForError(raw))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			continue
		}
		return nil, lastErr
	}
	if lastErr == nil {
		lastErr = errors.New("llm: exhausted retries")
	}
	return nil, lastErr
}

func (c *Client) do(ctx context.Context, body []byte) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", c.userAgent)
	return c.http.Do(r)
}

// buildRequestBody is the place where agent-level types meet the wire
// format. Centralised so the SSE path (when it lands) shares it.
func buildRequestBody(req *CreateRequest) ([]byte, error) {
	if req.Model == "" {
		return nil, errors.New("model is required")
	}
	wire := map[string]any{
		"model":  req.Model,
		"input":  ToInputItems(req.Messages),
		"stream": false,
	}
	if req.Instructions != "" {
		wire["instructions"] = req.Instructions
	}
	if len(req.Tools) > 0 {
		wire["tools"] = toToolWire(req.Tools)
		wire["tool_choice"] = "auto"
	}
	if req.MaxOutputTokens > 0 {
		wire["max_output_tokens"] = req.MaxOutputTokens
	}
	return json.Marshal(wire)
}

// ToInputItems converts the agent's flat history into Response API items.
// Exposed (capitalised) so callers can preview what they'll send for
// debugging without re-running buildRequestBody.
func ToInputItems(msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "user", "system", "developer":
			// "system" historically belongs in `instructions` but we accept
			// it here as a developer-role item so callers can interleave
			// system reminders mid-conversation if they want.
			out = append(out, map[string]any{
				"type":    "message",
				"role":    m.Role,
				"content": []map[string]any{{"type": "input_text", "text": m.Content}},
			})
		case "assistant":
			if m.Content != "" {
				out = append(out, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": []map[string]any{{"type": "output_text", "text": m.Content}},
				})
			}
			for _, c := range m.ToolCalls {
				out = append(out, map[string]any{
					"type":      "function_call",
					"call_id":   c.ID,
					"name":      c.Name,
					"arguments": c.Arguments,
				})
			}
		case "tool":
			out = append(out, map[string]any{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  m.Content,
			})
		}
	}
	return out
}

func toToolWire(tools []ToolDescriptor) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		// Response API uses a flat function-tool object, not the older
		// {type:"function", function:{...}} nesting Chat Completions used.
		entry := map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Parameters,
		}
		out = append(out, entry)
	}
	return out
}

// parseResponse extracts the assistant text + tool calls from a Response
// API body. The body is buffered fully (rather than json.Decode'd) so we
// can return Raw to the caller for audit.
func parseResponse(resp *http.Response) (*CreateResponse, error) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("llm: read response: %w", err)
	}
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
		} `json:"output"`
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
			Total  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("llm: decode response: %w (body=%s)", err, truncateForError(raw))
	}
	out := &CreateResponse{
		ID:         wire.ID,
		StatusCode: resp.StatusCode,
		Raw:        raw,
		Usage: Usage{
			InputTokens:  wire.Usage.Input,
			OutputTokens: wire.Usage.Output,
			TotalTokens:  wire.Usage.Total,
		},
	}
	var textBuf strings.Builder
	for _, item := range wire.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" {
					textBuf.WriteString(c.Text)
				}
			}
		case "function_call":
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: item.Args,
			})
		}
	}
	out.Content = textBuf.String()
	return out, nil
}

// truncateForError caps an upstream body for use in an error message. We
// never want a 1 MB upstream HTML page to drown the agent log.
func truncateForError(b []byte) string {
	const cap = 512
	if len(b) <= cap {
		return string(b)
	}
	return string(b[:cap]) + "..."
}
