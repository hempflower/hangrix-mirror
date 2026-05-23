// Package upstream defines the per-vendor adapter the llm_proxy handler
// dispatches to.
//
// Mock is the built-in mock LLM provider. It never makes real HTTP calls;
// instead it returns deterministic, text-only, non-streaming responses
// derived from the last user message in the conversation.
//
// Two modes:
//
//   - Default echo mode: wraps the last user text in a fixed template,
//     useful for "did the LLM get called?" smoke checks.
//
//   - Script mode: when the last user text starts with a special marker
//     prefix, the mock provider returns a predetermined response. This
//     lets e2e tests drive the agent to produce specific replies, tool
//     calls, or session-end events without a real LLM key.
//
// Markers:
//
//	!!!MOCK_RESPONSE:<text>  → returns <text> as the assistant message.
//	!!!MOCK_TOOL:<json>      → returns a single tool call; <json> must be
//	                            {"name":"...","arguments":"..."}.
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
)

// Mock is the built-in mock LLM adapter. It satisfies the Provider
// interface without making any external HTTP calls. A single instance
// is safe for concurrent use (it carries no mutable state).
type Mock struct{}

// NewMock returns a new Mock adapter ready for registration in the
// upstream Registry.
func NewMock() *Mock {
	return &Mock{}
}

func (m *Mock) Type() domain.ProviderType {
	return domain.ProviderTypeMock
}

// Respond returns a deterministic, text-only response based on the last
// user message in req.Input.
//
// Script-mode prefixes are checked first; when none match, the default
// echo template is used.
func (m *Mock) Respond(_ context.Context, req *Request) (*Response, error) {
	lastUser := lastUserText(req.Input)

	var (
		text      string
		toolCalls []ToolCall
	)

	switch {
	case strings.HasPrefix(lastUser, "!!!MOCK_RESPONSE:"):
		text = strings.TrimPrefix(lastUser, "!!!MOCK_RESPONSE:")

	case strings.HasPrefix(lastUser, "!!!MOCK_TOOL:"):
		payload := strings.TrimPrefix(lastUser, "!!!MOCK_TOOL:")
		var tc struct {
			Name string `json:"name"`
			Args string `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(payload), &tc); err != nil {
			text = fmt.Sprintf("mock: invalid tool call payload: %v", err)
		} else {
			toolCalls = []ToolCall{{
				ID:        stableID("mock_tc", tc.Name+"|"+tc.Args),
				Name:      tc.Name,
				Arguments: tc.Args,
			}}
			text = "mock: executing tool call " + tc.Name
		}

	default:
		// Default echo mode.
		truncated := lastUser
		const maxLen = 200
		if len(truncated) > maxLen {
			truncated = truncated[:maxLen] + "..."
		}
		text = fmt.Sprintf("This is a mock LLM response. The last user message was: %q", truncated)
	}

	mockID := stableID("mock_resp", lastUser)

	// Build a minimal Raw body that looks like a plausible Responses-API
	// response so audit logs stay self-describing.
	rawBody := map[string]any{
		"id": mockID,
		"output": []map[string]any{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": text},
				},
			},
		},
		"usage": map[string]any{
			"input_tokens":  1,
			"output_tokens": 1,
			"total_tokens":  2,
		},
	}
	raw, _ := json.Marshal(rawBody)

	return &Response{
		ID:         mockID,
		Text:       text,
		ToolCalls:  toolCalls,
		Usage:      Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		Raw:        raw,
		StatusCode: 200,
	}, nil
}

// lastUserText returns the text of the most recent user message in the
// input list, scanning from the end. Returns "" when no user message is
// found.
func lastUserText(items []InputItem) string {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == KindMessage && items[i].Role == "user" {
			return items[i].Text
		}
	}
	return ""
}

// stableID returns a deterministic, content-based identifier by hashing s
// with FNV-64a. Same (prefix, s) → same ID every time, enabling reproducible
// e2e / snapshot / replay scenarios.
func stableID(prefix, s string) string {
	h := fnv.New64a()
	h.Write([]byte(s))
	return fmt.Sprintf("%s_%x", prefix, h.Sum64())
}
