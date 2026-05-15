package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
)

// defaultAnthropicBaseURL is the canonical Anthropic Messages host. Used
// when a provider row of type `anthropic` has an empty BaseURL.
const defaultAnthropicBaseURL = "https://api.anthropic.com"

// anthropicAPIVersion is the wire version we negotiate. Hard-coded because
// translating across versions is not in scope for M6a; bumping it is a
// deliberate code change with a translator review.
const anthropicAPIVersion = "2023-06-01"

// defaultMaxTokens applies when the caller omits max_output_tokens.
// Anthropic requires max_tokens to be set; OpenAI's Responses API treats
// it as optional. 4096 matches the OpenAI default for gpt-4-class models
// and is a safe ceiling for the M6a smoke tests.
const defaultMaxTokens = 4096

// Sentinel errors surfaced by the anthropic translator and mapped to
// specific HTTP statuses by the proxy handler.
var (
	// errStreamingUnsupported is returned when a caller sets `stream:true`
	// against an anthropic provider. M6a only ships non-streaming for the
	// translation path; the openai/openai-compat paths support streaming.
	errStreamingUnsupported = errors.New("streaming not yet supported for anthropic")
	// errPathUnsupported fires for any anthropic request whose path is
	// not /v1/responses. The Anthropic translator only knows how to map
	// the Responses API today.
	errPathUnsupported = errors.New("path not supported for anthropic translator")
)

// forwardAnthropic translates an OpenAI Responses-API request into an
// Anthropic Messages request, calls the upstream, and translates the
// Messages response back. Scope is intentionally narrow: text-only
// in/out, non-streaming, /v1/responses only. Streaming is reported as a
// 501 by way of errStreamingUnsupported; other paths return
// errPathUnsupported.
func forwardAnthropic(
	ctx context.Context,
	client *http.Client,
	provider *domain.Provider,
	apiKey string,
	r *http.Request,
	body []byte,
	providerName string,
	stream bool,
) (*http.Response, error) {
	if stream {
		return nil, errStreamingUnsupported
	}
	tail := upstreamSuffix(r.URL.Path, providerName)
	// Trim trailing slash so /v1/responses/ matches /v1/responses.
	tail = strings.TrimRight(tail, "/")
	if tail != "/v1/responses" {
		return nil, errPathUnsupported
	}

	var reqBody map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &reqBody); err != nil {
			return nil, fmt.Errorf("decode responses request: %w", err)
		}
	}

	anthropicReq := translateRequestToAnthropic(reqBody)
	encoded, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("encode anthropic request: %w", err)
	}

	base := strings.TrimRight(provider.BaseURL, "/")
	if base == "" {
		base = defaultAnthropicBaseURL
	}
	url := base + "/v1/messages"

	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("build anthropic request: %w", err)
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "application/json")
	// Anthropic uses x-api-key + anthropic-version; do NOT set
	// Authorization: Bearer (different scheme, would just be ignored but
	// could leak the key through their access logs).
	upstreamReq.Header.Set("x-api-key", apiKey)
	upstreamReq.Header.Set("anthropic-version", anthropicAPIVersion)
	upstreamReq.ContentLength = int64(len(encoded))

	upstreamResp, err := client.Do(upstreamReq)
	if err != nil {
		return nil, fmt.Errorf("call anthropic: %w", err)
	}
	// We need to re-encode the body, which means buffering and replacing
	// upstreamResp. Always close the original body once read.
	defer upstreamResp.Body.Close()

	rawResp, err := io.ReadAll(upstreamResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read anthropic body: %w", err)
	}

	// Non-2xx: forward the error envelope as-is, but wrap it in the OpenAI
	// error shape so SDKs that key on `error.type` don't crash. Status code
	// is preserved.
	if upstreamResp.StatusCode >= 400 {
		out := translateAnthropicError(rawResp)
		return synthResponse(upstreamResp.StatusCode, out, upstreamResp.Header), nil
	}

	var anthropicResp map[string]any
	if len(rawResp) > 0 {
		if err := json.Unmarshal(rawResp, &anthropicResp); err != nil {
			return nil, fmt.Errorf("decode anthropic body: %w", err)
		}
	}
	translated := translateAnthropicResponse(anthropicResp)
	out, err := json.Marshal(translated)
	if err != nil {
		return nil, fmt.Errorf("encode responses body: %w", err)
	}

	return synthResponse(http.StatusOK, out, nil), nil
}

// translateRequestToAnthropic maps the relevant OpenAI Responses fields
// onto an Anthropic Messages request. Unknown fields are dropped; tools
// and image content are silently skipped per the M6a scope note.
func translateRequestToAnthropic(req map[string]any) map[string]any {
	out := map[string]any{
		"model":      getString(req, "model"),
		"max_tokens": defaultMaxTokens,
	}
	if v := getString(req, "instructions"); v != "" {
		out["system"] = v
	}
	if temp, ok := req["temperature"].(float64); ok {
		out["temperature"] = temp
	}
	if max, ok := req["max_output_tokens"].(float64); ok && max > 0 {
		out["max_tokens"] = int(max)
	}

	out["messages"] = extractMessages(req["input"])
	return out
}

// extractMessages handles the two shapes the Responses API accepts for
// `input`: a plain string (shorthand for one user message) or a list of
// typed message objects. For the list shape we extract text content
// blocks and skip everything else. Returns at least one user message so
// Anthropic doesn't 400 us on an empty conversation.
func extractMessages(input any) []map[string]any {
	switch v := input.(type) {
	case string:
		if v == "" {
			return []map[string]any{}
		}
		return []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": v}},
		}}
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			role := getString(obj, "role")
			if role == "" {
				role = "user"
			}
			// Anthropic only knows user/assistant; map system to user with
			// a leading marker so the upstream sees the content at all
			// (proper system-prompt handling happens via `instructions`,
			// not inline system messages).
			if role == "system" {
				continue
			}
			text := extractTextFromContent(obj["content"])
			if text == "" {
				continue
			}
			out = append(out, map[string]any{
				"role": role,
				"content": []map[string]any{
					{"type": "text", "text": text},
				},
			})
		}
		return out
	}
	return []map[string]any{}
}

// extractTextFromContent concatenates every text block in a Responses
// content array. Non-text blocks (tools, images, function calls) are
// dropped silently per the M6a scope. A bare string is also accepted
// because the wire shape isn't strictly enforced by the SDK in practice.
func extractTextFromContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			t := getString(obj, "type")
			// `text` (user) and `input_text` / `output_text` (Responses
			// typed variants) all carry a `text` field.
			if t != "text" && t != "input_text" && t != "output_text" {
				continue
			}
			b.WriteString(getString(obj, "text"))
		}
		return b.String()
	}
	return ""
}

// translateAnthropicResponse converts an Anthropic Messages reply into
// the Responses API shape the caller expected. The translation is
// best-effort: we synthesize the wrapping envelope but the actual content
// is a straight concatenation of text blocks. The `id` and `model` echo
// upstream so downstream SDKs can correlate.
func translateAnthropicResponse(resp map[string]any) map[string]any {
	id := getString(resp, "id")
	model := getString(resp, "model")
	text := concatTextBlocks(resp["content"])

	usage, _ := resp["usage"].(map[string]any)
	inputTokens := getInt32(usage, "input_tokens")
	outputTokens := getInt32(usage, "output_tokens")

	return map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": time.Now().Unix(),
		"status":     "completed",
		"model":      model,
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
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"total_tokens":  inputTokens + outputTokens,
		},
	}
}

// concatTextBlocks reduces an Anthropic `content` array down to a single
// string. Only `type:"text"` blocks contribute; tool_use and other block
// types are dropped — we don't surface tools in M6a.
func concatTextBlocks(content any) string {
	arr, ok := content.([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if getString(obj, "type") != "text" {
			continue
		}
		b.WriteString(getString(obj, "text"))
	}
	return b.String()
}

// translateAnthropicError wraps an upstream error payload in the OpenAI
// error envelope. We preserve the original message verbatim — the
// upstream's own diagnostics are more informative than anything we could
// fabricate locally.
func translateAnthropicError(raw []byte) []byte {
	msg := "anthropic upstream error"
	if len(raw) > 0 {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err == nil {
			if inner, ok := obj["error"].(map[string]any); ok {
				if m := getString(inner, "message"); m != "" {
					msg = m
				}
			}
		}
	}
	out, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "upstream_error",
		},
	})
	return out
}

// synthResponse builds an *http.Response that wraps an in-memory body so
// the handler can drain it through the same code path as a real upstream
// response. We seed Content-Type / Content-Length and copy any caller-
// supplied headers (e.g. ratelimit hints) verbatim.
func synthResponse(status int, body []byte, src http.Header) *http.Response {
	h := http.Header{}
	for k, vs := range src {
		// Skip transport-level headers that no longer apply once we've
		// fully buffered + re-encoded the body. Content-Length and
		// Transfer-Encoding in particular would lie.
		if _, hop := hopByHopHeaders[k]; hop {
			continue
		}
		if k == "Content-Length" || k == "Content-Type" {
			continue
		}
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	return &http.Response{
		Status:        http.StatusText(status),
		StatusCode:    status,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}
