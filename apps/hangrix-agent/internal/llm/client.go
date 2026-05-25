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

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/httpx"
)

// Message is the agent's flat in-memory message shape. Mirrors the
// HistoryItem in pkg/ipc but stays inside the LLM package boundary so we
// could swap out the IPC layer (e.g. for a unit test) without touching the
// runtime context.
//
// Reasoning + ReasoningSignature carry the chain-of-thought that "thinking"
// models (DeepSeek-Reasoner, OpenAI o-series, …) emit alongside Content.
// They are set ONLY on assistant messages; downstream providers reject the
// next turn if a reasoning block was elided, so the agent rounds-trip them
// verbatim. Roles other than "assistant" leave these empty.
//
// Kind is an agent-side sub-tag orthogonal to Role. Today the only non-empty
// value is KindSummary, which marks a compacted-session checkpoint placed by
// the compact_session tool. The runtime's Snapshot uses Kind to find the
// most recent summary and anchor the LLM-facing window there — preserving
// the full history for audit while keeping the working set small.
type Message struct {
	Role               string     // "system" | "user" | "assistant" | "tool"
	Kind               string     // optional sub-tag, e.g. KindSummary
	Content            string     // plain-text body
	Reasoning          string     // assistant: chain-of-thought summary
	ReasoningSignature string     // assistant: opaque verification token (encrypted_content)
	ToolCalls          []ToolCall // populated on assistant messages with function calls
	ToolCallID         string     // populated on tool messages, joins back to ToolCall.ID
}

// KindSummary marks a Message as a compacted-session checkpoint. Snapshot
// returns the slice starting at the latest KindSummary entry; older history
// stays in the underlying slice for audit but is no longer sent to the LLM.
const KindSummary = "summary"

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
// function_call items in the order they appeared. Reasoning carries the
// upstream's chain-of-thought block when the provider emitted one
// (DeepSeek-Reasoner, OpenAI o-series, …) — the agent stores it on the
// resulting assistant Message so the next turn round-trips it.
type CreateResponse struct {
	ID                 string
	Content            string
	Reasoning          string
	ReasoningSignature string
	ToolCalls          []ToolCall
	Usage              Usage
	StatusCode         int    // upstream HTTP status (always 2xx on success)
	Raw                []byte // unparsed response body — kept for audit, never logged here
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
// https://hangrix.example/api/llm/v1). The trailing slash is stripped
// here so callers don't have to be careful — concatenating
// endpoint+"/responses" must produce a valid URL. The proxy is one
// unified endpoint; provider routing is resolved server-side from the
// request body's `model` field.
func New(endpoint, token string) *Client {
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		// 5-minute non-stream cap matches the proxy's upstreamTimeout, so a
		// timeout we see here means the proxy timed out too — we won't be
		// "stealing" a still-running upstream request. httpx.NewClient
		// honours HANGRIX_INSECURE_SKIP_TLS_VERIFY for the missing-CA
		// escape hatch.
		http:      httpx.NewClient(5 * time.Minute),
		userAgent: "hangrix-agent/0.1",
	}
}

// Create issues one non-streaming /responses call. Retries with
// exponential backoff on transport errors and 5xx; 4xx (except 429)
// returns immediately because retrying a malformed request just wastes
// quota. 429 is retried with the same backoff because the proxy may
// surface upstream rate-limit signals that way.
//
// Attempts are intentionally generous: a flaky upstream / proxy
// hiccup should never kill an in-flight session. The total wall-time
// ceiling is bounded by the per-call 5-minute context plus the agent's
// outer context — if either fires we bail with ctx.Err().
func (c *Client) Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error) {
	body, err := buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("llm: build request: %w", err)
	}

	const (
		maxAttempts  = 10
		maxBackoffMS = 30000 // 30s cap so one retry chain doesn't stall a turn for hours
	)
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Exponential backoff capped at 30s. No jitter — there's
			// only one client per session, so the thundering-herd
			// argument that motivates jitter on fleet retries does
			// not apply here.
			backoffMS := math.Pow(2, float64(attempt-1)) * 500
			if backoffMS > maxBackoffMS {
				backoffMS = maxBackoffMS
			}
			delay := time.Duration(backoffMS) * time.Millisecond
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
		// Kind=summary is a special agent-side marker, not a wire role.
		// Serialise it as a user-role text block wrapped in a clearly-
		// labelled tag so the LLM treats it as authoritative context
		// from a prior compacted segment of this same session.
		if m.Kind == KindSummary {
			text := "<previous_session_summary>\n" + m.Content + "\n</previous_session_summary>"
			out = append(out, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []map[string]any{{"type": "input_text", "text": text}},
			})
			continue
		}
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
			// Reasoning items must precede the assistant text + tool
			// calls — that's the spec order, and "thinking" providers
			// (DeepSeek-Reasoner, OpenAI o-series) reject any history
			// that elides the prior turn's reasoning_content.
			if m.Reasoning != "" {
				item := map[string]any{
					"type": "reasoning",
					"summary": []map[string]any{
						{"type": "summary_text", "text": m.Reasoning},
					},
				}
				if m.ReasoningSignature != "" {
					item["encrypted_content"] = m.ReasoningSignature
				}
				out = append(out, item)
			}
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
			Summary []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"summary"`
			EncryptedContent string `json:"encrypted_content"`
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
	var textBuf, reasonBuf strings.Builder
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
		case "reasoning":
			for _, s := range item.Summary {
				if s.Type == "" || s.Type == "summary_text" {
					reasonBuf.WriteString(s.Text)
				}
			}
			if item.EncryptedContent != "" {
				// Multiple reasoning blocks would each carry their own
				// signature in principle; we keep the last non-empty one
				// — that's typically the only one a server emits anyway.
				out.ReasoningSignature = item.EncryptedContent
			}
		}
	}
	out.Content = textBuf.String()
	out.Reasoning = reasonBuf.String()
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
