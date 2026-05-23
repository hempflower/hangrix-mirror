package upstream_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_proxy/upstream"
)

// TestMockDefaultEcho verifies the default echo mode: when the last user
// message has no special marker, the mock wraps it in a fixed template.
func TestMockDefaultEcho(t *testing.T) {
	m := upstream.NewMock()

	resp, err := m.Respond(context.Background(), &upstream.Request{
		Model: "mock-model",
		Input: []upstream.InputItem{
			{Kind: upstream.KindMessage, Role: "user", Text: "hello world"},
		},
	})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if resp.Text == "" {
		t.Fatalf("expected non-empty response text")
	}
	if !contains(resp.Text, "hello world") {
		t.Errorf("response should echo user text, got: %s", resp.Text)
	}
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode=%d, want 200", resp.StatusCode)
	}
	if resp.ID == "" {
		t.Error("expected non-empty ID")
	}
	if resp.Usage.TotalTokens == 0 {
		t.Error("expected non-zero usage")
	}
}

// TestMockScriptResponse verifies the !!!MOCK_RESPONSE: marker produces
// exactly the text that follows the prefix.
func TestMockScriptResponse(t *testing.T) {
	m := upstream.NewMock()

	resp, err := m.Respond(context.Background(), &upstream.Request{
		Model: "mock-model",
		Input: []upstream.InputItem{
			{Kind: upstream.KindMessage, Role: "user", Text: "!!!MOCK_RESPONSE:This is a scripted reply"},
		},
	})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if resp.Text != "This is a scripted reply" {
		t.Errorf("Text=%q, want %q", resp.Text, "This is a scripted reply")
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(resp.ToolCalls))
	}
}

// TestMockScriptToolCall verifies the !!!MOCK_TOOL: marker produces a
// correctly populated ToolCall.
func TestMockScriptToolCall(t *testing.T) {
	m := upstream.NewMock()

	resp, err := m.Respond(context.Background(), &upstream.Request{
		Model: "mock-model",
		Input: []upstream.InputItem{
			{Kind: upstream.KindMessage, Role: "user",
				Text: `!!!MOCK_TOOL:{"name":"issue_comment","arguments":"{\"body\":\"hello\"}"}`},
		},
	})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.Name != "issue_comment" {
		t.Errorf("tool name=%q, want %q", tc.Name, "issue_comment")
	}
	if tc.Arguments != `{"body":"hello"}` {
		t.Errorf("tool args=%q, want %q", tc.Arguments, `{"body":"hello"}`)
	}
}

// TestMockScriptToolCallInvalid verifies that a malformed !!!MOCK_TOOL:
// payload does not panic and surfaces an error message in Text.
func TestMockScriptToolCallInvalid(t *testing.T) {
	m := upstream.NewMock()

	resp, err := m.Respond(context.Background(), &upstream.Request{
		Model: "mock-model",
		Input: []upstream.InputItem{
			{Kind: upstream.KindMessage, Role: "user",
				Text: `!!!MOCK_TOOL:not-json`},
		},
	})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if !contains(resp.Text, "invalid") {
		t.Errorf("expected error message in Text, got: %s", resp.Text)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("expected no tool calls on parse error, got %d", len(resp.ToolCalls))
	}
}

// TestMockLastUserText verifies the mock correctly finds the last user
// message when the input list contains multiple items of different kinds.
func TestMockLastUserText(t *testing.T) {
	m := upstream.NewMock()

	resp, err := m.Respond(context.Background(), &upstream.Request{
		Model:        "mock-model",
		Instructions: "system prompt",
		Input: []upstream.InputItem{
			{Kind: upstream.KindMessage, Role: "user", Text: "first question"},
			{Kind: upstream.KindMessage, Role: "assistant", Text: "first answer"},
			{Kind: upstream.KindMessage, Role: "user", Text: "second question"},
		},
	})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	// Should echo "second question", not "first question".
	if !contains(resp.Text, "second question") {
		t.Errorf("expected last user message in echo, got: %s", resp.Text)
	}
	if contains(resp.Text, "first question") {
		t.Errorf("should NOT contain first user message: %s", resp.Text)
	}
}

// TestMockRawIsValidJSON verifies the Raw field is valid JSON and carries
// the expected structure.
func TestMockRawIsValidJSON(t *testing.T) {
	m := upstream.NewMock()

	resp, err := m.Respond(context.Background(), &upstream.Request{
		Model: "mock-model",
		Input: []upstream.InputItem{
			{Kind: upstream.KindMessage, Role: "user", Text: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(resp.Raw, &raw); err != nil {
		t.Fatalf("Raw is not valid JSON: %v\n%s", err, resp.Raw)
	}
	if raw["id"] == nil {
		t.Error("Raw missing 'id' field")
	}
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
