package upstream_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_proxy/upstream"
)

// TestDefaultRegistryCoversEveryProviderType locks in that
// upstream.Default() registers an adapter for every ProviderType the
// domain accepts. A new ProviderType added without a matching adapter
// fails loudly here — much better than discovering it when a session
// 501s in production.
func TestDefaultRegistryCoversEveryProviderType(t *testing.T) {
	reg := upstream.Default()
	for _, tp := range []domain.ProviderType{
		domain.ProviderTypeOpenAI,
		domain.ProviderTypeOpenAICompat,
		domain.ProviderTypeAnthropic,
		domain.ProviderTypeMock,
	} {
		p, ok := reg.Lookup(tp)
		if !ok {
			t.Errorf("default registry missing adapter for type %q", tp)
			continue
		}
		if p.Type() != tp {
			t.Errorf("adapter for %q reports Type()=%q", tp, p.Type())
		}
	}
}

// TestNewRegistryPanicsOnDuplicate documents the loud-failure contract.
func TestNewRegistryPanicsOnDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("expected NewRegistry to panic on duplicate type")
		}
	}()
	_ = upstream.NewRegistry(upstream.NewOpenAI(), upstream.NewOpenAI())
}

// ---- OpenAI adapter (Responses-API native) ----

// TestOpenAIRespondTranslatesInputItems verifies the OpenAI adapter
// emits a Responses-API request that round-trips every InputKind, and
// that response parsing reconstructs Text / Reasoning / ToolCalls /
// Usage correctly.
func TestOpenAIRespondTranslatesInputItems(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_1",
			"output": []map[string]any{
				{"type": "reasoning", "summary": []map[string]any{{"type": "summary_text", "text": "thought"}}},
				{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": "answer"}}},
				{"type": "function_call", "call_id": "tc1", "name": "read", "arguments": `{"p":"/x"}`},
			},
			"usage": map[string]any{
				"input_tokens": 10, "output_tokens": 20, "total_tokens": 30,
				"output_tokens_details": map[string]any{"reasoning_tokens": 8},
			},
		})
	}))
	t.Cleanup(srv.Close)

	temp := 0.5
	req := &upstream.Request{
		Model:           "gpt-test",
		Instructions:    "you are helpful",
		Temperature:     &temp,
		ReasoningEffort: "medium",
		Input: []upstream.InputItem{
			{Kind: upstream.KindMessage, Role: "user", Text: "hi"},
			{Kind: upstream.KindReasoning, Reasoning: "prior thought", ReasoningSignature: "sig"},
			{Kind: upstream.KindMessage, Role: "assistant", Text: "prior reply"},
			{Kind: upstream.KindToolCall, ToolCallID: "tc0", ToolName: "noop", ToolArgs: `{}`},
			{Kind: upstream.KindToolResult, ToolCallID: "tc0", ToolResult: "done"},
		},
		Tools: []upstream.Tool{
			{Name: "read", Description: "read a file", Parameters: map[string]any{"type": "object"}},
		},
		APIKey:  "x",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	}

	resp, err := upstream.NewOpenAI().Respond(context.Background(), req)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}

	// Request-side: every InputItem produced exactly one Responses-API
	// item in the same order.
	inputArr, _ := seen["input"].([]any)
	if len(inputArr) != 5 {
		t.Fatalf("expected 5 input items in outgoing body, got %d: %s", len(inputArr), seen)
	}
	wantTypes := []string{"message", "reasoning", "message", "function_call", "function_call_output"}
	for i, want := range wantTypes {
		it, _ := inputArr[i].(map[string]any)
		if it["type"] != want {
			t.Errorf("input[%d].type = %v, want %s", i, it["type"], want)
		}
	}
	// Reasoning signature must round-trip on the outbound request.
	if rs, _ := inputArr[1].(map[string]any); rs["encrypted_content"] != "sig" {
		t.Errorf("encrypted_content lost on reasoning item: %v", rs)
	}
	if seen["instructions"] != "you are helpful" {
		t.Errorf("instructions=%v", seen["instructions"])
	}
	if seen["temperature"] != 0.5 {
		t.Errorf("temperature=%v", seen["temperature"])
	}
	if reasoning, _ := seen["reasoning"].(map[string]any); reasoning["effort"] != "medium" {
		t.Errorf("reasoning.effort=%v", reasoning)
	}

	// Response-side: typed Response captures all three output kinds.
	if resp.Text != "answer" {
		t.Errorf("Text=%q, want %q", resp.Text, "answer")
	}
	if resp.Reasoning != "thought" {
		t.Errorf("Reasoning=%q, want %q", resp.Reasoning, "thought")
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read" {
		t.Errorf("ToolCalls=%+v", resp.ToolCalls)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 20 ||
		resp.Usage.ReasoningTokens != 8 || resp.Usage.TotalTokens != 30 {
		t.Errorf("Usage=%+v", resp.Usage)
	}
}

// TestOpenAIRespondReportsUpstreamError verifies that a non-2xx
// upstream surfaces as a typed UpstreamError carrying the original
// status code so the handler can mirror it.
func TestOpenAIRespondReportsUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
	}))
	t.Cleanup(srv.Close)
	_, err := upstream.NewOpenAI().Respond(context.Background(), &upstream.Request{
		Model: "m", APIKey: "x", BaseURL: srv.URL, Client: srv.Client(),
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	var ue *upstream.UpstreamError
	if !errors.As(err, &ue) || ue.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("err=%T %v, want *UpstreamError with 429", err, err)
	}
}

// ---- OpenAICompat adapter (Chat Completions) ----

// TestOpenAICompatPreservesReasoningContent is the multi-turn DeepSeek
// scenario the v2 typed adapter must support: a prior-turn KindReasoning
// item is folded onto the next assistant message's reasoning_content
// field in the outgoing chat-completions request body, AND the
// response's reasoning_content is captured into Response.Reasoning so
// the wire layer surfaces it as a `reasoning` output item.
func TestOpenAICompatPreservesReasoningContent(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-1",
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":              "assistant",
					"content":           "final",
					"reasoning_content": "fresh thinking",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12,
				"completion_tokens_details": map[string]any{"reasoning_tokens": 3},
			},
		})
	}))
	t.Cleanup(srv.Close)

	req := &upstream.Request{
		Model:        "deepseek-reasoner",
		Instructions: "be brief",
		Input: []upstream.InputItem{
			{Kind: upstream.KindMessage, Role: "user", Text: "q1"},
			// Prior turn's reasoning + assistant text — must collapse
			// into one assistant message with both fields.
			{Kind: upstream.KindReasoning, Reasoning: "prior thought"},
			{Kind: upstream.KindMessage, Role: "assistant", Text: "prior answer"},
			{Kind: upstream.KindMessage, Role: "user", Text: "q2"},
		},
		APIKey:  "x",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	}
	resp, err := upstream.NewOpenAICompat().Respond(context.Background(), req)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}

	msgs, _ := seen["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 chat messages (system, user, assistant-with-reasoning, user), got %d: %v", len(msgs), msgs)
	}
	// system / user / assistant / user
	system, _ := msgs[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "be brief" {
		t.Errorf("system message = %v", system)
	}
	assistant, _ := msgs[2].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("expected assistant at index 2, got %v", assistant)
	}
	if assistant["content"] != "prior answer" {
		t.Errorf("assistant content = %v", assistant["content"])
	}
	if assistant["reasoning_content"] != "prior thought" {
		t.Errorf("assistant reasoning_content = %v; full msg=%v", assistant["reasoning_content"], assistant)
	}

	// Response side: fresh reasoning_content lands on resp.Reasoning.
	if resp.Text != "final" {
		t.Errorf("Text=%q", resp.Text)
	}
	if resp.Reasoning != "fresh thinking" {
		t.Errorf("Reasoning=%q", resp.Reasoning)
	}
	if resp.Usage.ReasoningTokens != 3 {
		t.Errorf("ReasoningTokens=%d", resp.Usage.ReasoningTokens)
	}
}

// TestOpenAICompatToolCallsCollapse verifies a function_call following
// an assistant text item folds into the SAME chat message's tool_calls
// array — chat-completions can't represent two consecutive assistant
// messages, so the merge is load-bearing.
func TestOpenAICompatToolCallsCollapse(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-2",
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	t.Cleanup(srv.Close)
	req := &upstream.Request{
		Model: "m", APIKey: "x", BaseURL: srv.URL, Client: srv.Client(),
		Input: []upstream.InputItem{
			{Kind: upstream.KindMessage, Role: "user", Text: "compute"},
			{Kind: upstream.KindMessage, Role: "assistant", Text: "calling tool"},
			{Kind: upstream.KindToolCall, ToolCallID: "tc1", ToolName: "calc", ToolArgs: `{"x":1}`},
			{Kind: upstream.KindToolResult, ToolCallID: "tc1", ToolResult: "42"},
		},
	}
	if _, err := upstream.NewOpenAICompat().Respond(context.Background(), req); err != nil {
		t.Fatalf("respond: %v", err)
	}
	msgs, _ := seen["messages"].([]any)
	// user / assistant(content+tool_calls) / tool
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %v", len(msgs), msgs)
	}
	assistant, _ := msgs[1].(map[string]any)
	if assistant["content"] != "calling tool" {
		t.Errorf("assistant content = %v", assistant["content"])
	}
	tc, _ := assistant["tool_calls"].([]any)
	if len(tc) != 1 {
		t.Fatalf("expected one tool_call, got %v", assistant["tool_calls"])
	}
	tool, _ := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "tc1" || tool["content"] != "42" {
		t.Errorf("tool message = %v", tool)
	}
}

// TestOpenAICompatBaseURLRequired pins the load-bearing distinction
// between openai (defaults to api.openai.com) and openai-compat (must
// have a BaseURL explicitly configured).
func TestOpenAICompatBaseURLRequired(t *testing.T) {
	_, err := upstream.NewOpenAICompat().Respond(context.Background(), &upstream.Request{
		Model: "m", APIKey: "x", Client: http.DefaultClient,
	})
	if err == nil || !errors.Is(err, upstream.ErrBaseURLRequired) {
		t.Fatalf("err=%v, want ErrBaseURLRequired", err)
	}
}

// ---- Anthropic adapter ----

// TestAnthropicRespondTranslatesReasoningEffort verifies the new typed
// adapter produces an Anthropic body with the right thinking block,
// bumped max_tokens, dropped temperature, and returns thinking blocks
// as Response.Reasoning (with signature captured).
func TestAnthropicRespondTranslatesReasoningEffort(t *testing.T) {
	cases := []struct {
		effort           string
		wantBudget       int
		wantMaxTokensMin int
	}{
		{"low", 1024, 5120},
		{"medium", 4096, 8192},
		{"high", 16384, 20480},
	}
	for _, c := range cases {
		t.Run(c.effort, func(t *testing.T) {
			var seen map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &seen)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":    "msg_1",
					"model": "claude-test",
					"content": []map[string]any{
						{"type": "thinking", "thinking": "thought", "signature": "sig"},
						{"type": "text", "text": "final"},
					},
					"usage": map[string]any{"input_tokens": 3, "output_tokens": 7},
				})
			}))
			t.Cleanup(srv.Close)

			temp := 0.5
			req := &upstream.Request{
				Model:           "claude-test",
				Temperature:     &temp,
				ReasoningEffort: c.effort,
				Input: []upstream.InputItem{
					{Kind: upstream.KindMessage, Role: "user", Text: "hi"},
				},
				APIKey: "x", BaseURL: srv.URL, Client: srv.Client(),
			}
			resp, err := upstream.NewAnthropic().Respond(context.Background(), req)
			if err != nil {
				t.Fatalf("respond: %v", err)
			}

			thinking, _ := seen["thinking"].(map[string]any)
			if thinking == nil {
				t.Fatalf("expected thinking block; body=%v", seen)
			}
			if got, _ := thinking["budget_tokens"].(float64); int(got) != c.wantBudget {
				t.Errorf("budget_tokens=%v, want %d", got, c.wantBudget)
			}
			if got, _ := seen["max_tokens"].(float64); int(got) < c.wantMaxTokensMin {
				t.Errorf("max_tokens=%v, want ≥ %d", got, c.wantMaxTokensMin)
			}
			if _, hasTemp := seen["temperature"]; hasTemp {
				t.Errorf("temperature must be dropped with thinking; body=%v", seen)
			}

			if resp.Text != "final" {
				t.Errorf("Text=%q", resp.Text)
			}
			if resp.Reasoning != "thought" {
				t.Errorf("Reasoning=%q", resp.Reasoning)
			}
			if resp.ReasoningSignature != "sig" {
				t.Errorf("ReasoningSignature=%q", resp.ReasoningSignature)
			}
		})
	}
}

// TestAnthropicRoundTripsThinkingOnSubsequentTurn verifies a
// KindReasoning item on the inbound request folds into the next
// assistant message's `thinking` content block, preserving the
// signature — required by Anthropic strict-mode verification.
func TestAnthropicRoundTripsThinkingOnSubsequentTurn(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_2", "model": "m",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
			"usage":   map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)

	req := &upstream.Request{
		Model: "m", APIKey: "x", BaseURL: srv.URL, Client: srv.Client(),
		ReasoningEffort: "medium", // keep thinking enabled on this turn too
		Input: []upstream.InputItem{
			{Kind: upstream.KindMessage, Role: "user", Text: "q1"},
			{Kind: upstream.KindReasoning, Reasoning: "prior thought", ReasoningSignature: "prior-sig"},
			{Kind: upstream.KindMessage, Role: "assistant", Text: "prior answer"},
			{Kind: upstream.KindMessage, Role: "user", Text: "q2"},
		},
	}
	if _, err := upstream.NewAnthropic().Respond(context.Background(), req); err != nil {
		t.Fatalf("respond: %v", err)
	}
	msgs, _ := seen["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (user, assistant, user), got %d: %v", len(msgs), msgs)
	}
	assistant, _ := msgs[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("expected assistant at index 1, got %v", assistant)
	}
	blocks, _ := assistant["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected [thinking, text] content blocks, got %v", blocks)
	}
	thinkBlock, _ := blocks[0].(map[string]any)
	if thinkBlock["type"] != "thinking" || thinkBlock["thinking"] != "prior thought" {
		t.Errorf("thinking block = %v", thinkBlock)
	}
	if thinkBlock["signature"] != "prior-sig" {
		t.Errorf("thinking signature lost: %v", thinkBlock)
	}
	textBlock, _ := blocks[1].(map[string]any)
	if textBlock["type"] != "text" || textBlock["text"] != "prior answer" {
		t.Errorf("text block = %v", textBlock)
	}
}

// TestAnthropicRoundTripsToolUse covers the full tool-calling flow on
// the Anthropic adapter: tools array forwarded on the request, a
// prior turn's tool_call + tool_result history mapped to
// (assistant.tool_use, user.tool_result) content blocks, and an
// upstream tool_use response decoded into Response.ToolCalls.
func TestAnthropicRoundTripsToolUse(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		// Upstream replies with one text block + one tool_use block,
		// in that order — matches how Anthropic emits real responses.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_2",
			"content": []map[string]any{
				{"type": "text", "text": "looking it up"},
				{
					"type":  "tool_use",
					"id":    "toolu_xyz",
					"name":  "issue_read",
					"input": map[string]any{"issue_number": 7},
				},
			},
			"usage": map[string]any{"input_tokens": 12, "output_tokens": 4},
		})
	}))
	t.Cleanup(srv.Close)

	req := &upstream.Request{
		Model:   "claude-x",
		APIKey:  "k",
		BaseURL: srv.URL,
		Client:  srv.Client(),
		Tools: []upstream.Tool{{
			Name:        "issue_read",
			Description: "Read an issue.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"issue_number": map[string]any{"type": "integer"}},
				"required":   []string{"issue_number"},
			},
		}},
		Input: []upstream.InputItem{
			{Kind: upstream.KindMessage, Role: "user", Text: "look up issue 7"},
			{Kind: upstream.KindMessage, Role: "assistant", Text: "calling tool"},
			{Kind: upstream.KindToolCall, ToolCallID: "toolu_prev", ToolName: "issue_read", ToolArgs: `{"issue_number":1}`},
			{Kind: upstream.KindToolResult, ToolCallID: "toolu_prev", ToolResult: `{"title":"hello"}`},
		},
	}
	resp, err := upstream.NewAnthropic().Respond(context.Background(), req)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}

	// ---- request side ----
	tools, _ := seen["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool advertised, got %v", seen["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "issue_read" {
		t.Errorf("tool name = %v", tool["name"])
	}
	// Anthropic uses input_schema, not parameters.
	if _, hasSchema := tool["input_schema"]; !hasSchema {
		t.Errorf("tool missing input_schema: %v", tool)
	}
	if _, hasParams := tool["parameters"]; hasParams {
		t.Errorf("tool should not carry OpenAI-style `parameters`: %v", tool)
	}

	msgs, _ := seen["messages"].([]any)
	// user(text) / assistant(text + tool_use) / user(tool_result)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %v", len(msgs), msgs)
	}
	assistant, _ := msgs[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Errorf("msg[1] role = %v, want assistant", assistant["role"])
	}
	aBlocks, _ := assistant["content"].([]any)
	// Expected order: text, tool_use (thinking would come first if present).
	if len(aBlocks) != 2 {
		t.Fatalf("assistant blocks = %v", aBlocks)
	}
	toolUse, _ := aBlocks[1].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "toolu_prev" || toolUse["name"] != "issue_read" {
		t.Errorf("tool_use block = %v", toolUse)
	}
	// input is an object on the wire (NOT a re-quoted JSON string).
	input, ok := toolUse["input"].(map[string]any)
	if !ok {
		t.Errorf("tool_use.input must be an object, got %T: %v", toolUse["input"], toolUse["input"])
	} else if input["issue_number"].(float64) != 1 {
		t.Errorf("tool_use.input = %v", input)
	}

	userResult, _ := msgs[2].(map[string]any)
	uBlocks, _ := userResult["content"].([]any)
	resultBlock, _ := uBlocks[0].(map[string]any)
	if resultBlock["type"] != "tool_result" || resultBlock["tool_use_id"] != "toolu_prev" {
		t.Errorf("tool_result block = %v", resultBlock)
	}
	if resultBlock["content"] != `{"title":"hello"}` {
		t.Errorf("tool_result content = %v", resultBlock["content"])
	}

	// ---- response side ----
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 ToolCall in response, got %v", resp.ToolCalls)
	}
	got := resp.ToolCalls[0]
	if got.ID != "toolu_xyz" || got.Name != "issue_read" {
		t.Errorf("ToolCall = %+v", got)
	}
	// Arguments must be a JSON string the rest of the agent's pipeline
	// can re-encode untouched.
	if got.Arguments != `{"issue_number":7}` {
		t.Errorf("ToolCall.Arguments = %q, want {\"issue_number\":7}", got.Arguments)
	}
	if resp.Text != "looking it up" {
		t.Errorf("resp.Text = %q", resp.Text)
	}
}

// TestAnthropicNullInputNormalised verifies that a null `input` field
// in an upstream tool_use block (or a null ToolArgs string on an inbound
// function_call item) is normalised to {} rather than leaking the string
// "null" into the pipeline.  Without this, the downstream agent receives
// unparseable tool arguments whenever the upstream (or client) emits a
// null value instead of the expected JSON object.
func TestAnthropicNullInputNormalised(t *testing.T) {
	// ---- Response side: upstream returns "input": null ----
	body := `{"id":"msg_n","model":"m","content":[{"type":"tool_use","id":"t1","name":"f","input":null}],"usage":{"input_tokens":1,"output_tokens":2}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	resp, err := upstream.NewAnthropic().Respond(context.Background(), &upstream.Request{
		Model: "m", APIKey: "k", BaseURL: srv.URL, Client: srv.Client(),
		Input: []upstream.InputItem{{Kind: upstream.KindMessage, Role: "user", Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Arguments != "{}" {
		t.Errorf("Arguments = %q, want %q — null should be normalised to {}", resp.ToolCalls[0].Arguments, "{}")
	}

	// ---- Request side: client sends ToolArgs = "null" ----
	var seen map[string]any
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_n2", "model": "m",
			"content": []map[string]any{{"type": "text", "text": "ok"}},
			"usage":   map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	t.Cleanup(srv2.Close)

	_, err = upstream.NewAnthropic().Respond(context.Background(), &upstream.Request{
		Model: "m", APIKey: "k", BaseURL: srv2.URL, Client: srv2.Client(),
		Input: []upstream.InputItem{
			{Kind: upstream.KindMessage, Role: "user", Text: "hi"},
			{Kind: upstream.KindToolCall, ToolCallID: "tc1", ToolName: "f", ToolArgs: "null"},
		},
	})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	msgs, _ := seen["messages"].([]any)
	assistant, _ := msgs[len(msgs)-1].(map[string]any)
	blocks, _ := assistant["content"].([]any)
	for _, b := range blocks {
		block, _ := b.(map[string]any)
		if block["type"] == "tool_use" {
			input, ok := block["input"].(map[string]any)
			if !ok {
				t.Errorf("tool_use.input must be an object, got %T: %v", block["input"], block["input"])
			}
			_ = input
		}
	}
}

// ---- wire converters ----

// TestParseAndMarshalRoundTrip verifies the public wire shape
// round-trips through ParseResponsesAPIRequest → Request → ... → Response →
// MarshalResponsesAPIResponse with the expected fields populated.
func TestParseAndMarshalRoundTrip(t *testing.T) {
	in := []byte(`{
		"model":"m","instructions":"sys",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"prior"}],"encrypted_content":"sig"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"prior reply"}]}
		],
		"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}],
		"reasoning":{"effort":"low"},
		"max_output_tokens":100
	}`)
	req, stream, err := upstream.ParseResponsesAPIRequest(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if stream {
		t.Errorf("stream=true unexpected")
	}
	if req.Model != "m" || req.Instructions != "sys" || req.ReasoningEffort != "low" {
		t.Errorf("req=%+v", req)
	}
	if len(req.Input) != 3 {
		t.Fatalf("input length=%d, want 3", len(req.Input))
	}
	if req.Input[1].Kind != upstream.KindReasoning || req.Input[1].Reasoning != "prior" || req.Input[1].ReasoningSignature != "sig" {
		t.Errorf("reasoning item lost fields: %+v", req.Input[1])
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "read" {
		t.Errorf("tools=%+v", req.Tools)
	}
	if req.MaxOutputTokens != 100 {
		t.Errorf("MaxOutputTokens=%d", req.MaxOutputTokens)
	}

	// Marshal side.
	resp := &upstream.Response{
		ID: "r1", Text: "answer", Reasoning: "thinking", ReasoningSignature: "rs-sig",
		ToolCalls: []upstream.ToolCall{{ID: "tc", Name: "read", Arguments: `{}`}},
		Usage:     upstream.Usage{PromptTokens: 5, CompletionTokens: 7, ReasoningTokens: 2, TotalTokens: 12},
	}
	out, err := upstream.MarshalResponsesAPIResponse(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	_ = json.Unmarshal(out, &decoded)
	outArr, _ := decoded["output"].([]any)
	if len(outArr) != 3 {
		t.Fatalf("output items=%d, want 3 (reasoning, message, function_call): %s", len(outArr), out)
	}
	if first, _ := outArr[0].(map[string]any); first["type"] != "reasoning" || first["encrypted_content"] != "rs-sig" {
		t.Errorf("reasoning output item lost fields: %v", first)
	}
	usage, _ := decoded["usage"].(map[string]any)
	if detail, _ := usage["output_tokens_details"].(map[string]any); detail == nil ||
		int(detail["reasoning_tokens"].(float64)) != 2 {
		t.Errorf("reasoning_tokens not surfaced under output_tokens_details: %v", usage)
	}
}
