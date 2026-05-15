// Package upstream defines the per-vendor adapter the llm_proxy handler
// dispatches to. Each registered Provider owns one row in the type
// switch — it takes a structured Request, talks to its vendor over
// whatever wire format that vendor speaks, and returns a structured
// Response. The handler is responsible for translating between the
// public OpenAI Responses-API wire shape and the typed
// Request/Response; adapters never touch HTTP request/response objects
// of the inbound call.
//
// One Provider implementation per domain.ProviderType. The Registry is
// built once at module-load and shared by every request; Provider
// impls must be stateless (no per-request state on the struct) so a
// single instance can fan out concurrently.
package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
)

// Provider is the per-vendor adapter the proxy delegates to.
//
// The interface is intentionally narrow: a single Respond call covers
// the entire round-trip. Usage extraction, format translation, and
// retry policy all live behind it; the caller (handler) never has to
// reach into HTTP plumbing to find a token count.
type Provider interface {
	Type() domain.ProviderType
	Respond(ctx context.Context, req *Request) (*Response, error)
}

// Request is the structured input to an upstream model. It captures
// the semantic shape of a chat-with-tools call without leaking HTTP or
// any vendor-specific wire format. Adapters translate fields here into
// whatever the upstream speaks.
type Request struct {
	// Model is the upstream model identifier. Validated by the handler
	// against the token binding + provider allow-list before dispatch.
	Model string

	// Instructions is the system prompt block. Empty when the caller
	// omitted `instructions` from the Responses-API request body.
	Instructions string

	// Input is the conversation history in chronological order. See
	// InputItem for the per-item shape.
	Input []InputItem

	// Tools is the function-tool catalogue exposed to the model.
	Tools []Tool

	// MaxOutputTokens caps the upstream's per-call output budget.
	// Zero means "let the upstream apply its default".
	MaxOutputTokens int

	// Temperature is the OpenAI-style sampling temperature. Adapters
	// that don't support it (e.g. Anthropic with thinking enabled)
	// drop the field rather than 400ing upstream.
	Temperature *float64

	// ReasoningEffort mirrors OpenAI's `reasoning.effort` enum:
	// "" / "minimal" / "low" / "medium" / "high". Adapters with a
	// native reasoning knob (Anthropic thinking, DeepSeek reasoner,
	// OpenAI o-series) translate this to their own setting; others
	// ignore it.
	ReasoningEffort string

	// APIKey is the decrypted upstream credential the adapter places
	// on the outgoing request. Bearer-format for OpenAI-family;
	// x-api-key for Anthropic.
	APIKey string

	// BaseURL is the upstream base (no path). Adapters append their
	// own native paths (e.g. "/v1/chat/completions") to this.
	BaseURL string

	// Client is the HTTP client to use for the upstream call. Owned by
	// the handler so timeouts / transport policy live in one place.
	Client *http.Client
}

// InputItem is one chronological item in a conversation. Kind
// discriminates which subset of the fields below is populated.
type InputItem struct {
	Kind InputKind

	// Role is populated when Kind == KindMessage. One of "user" /
	// "assistant" / "system" / "developer".
	Role string

	// Text is the plain-text content. Populated for KindMessage and
	// KindToolResult.
	Text string

	// Reasoning is the chain-of-thought / thinking text. Populated for
	// KindReasoning. On the next round-trip the adapter MUST echo this
	// back (DeepSeek's reasoning_content, Anthropic's thinking blocks)
	// so the model retains its prior context.
	Reasoning string

	// ReasoningSignature, when non-empty, is the opaque verification
	// token the upstream emitted alongside its reasoning (Anthropic
	// signed thinking blocks). Adapters that produce signed reasoning
	// store it here on response items; adapters that consume signed
	// reasoning round-trip the field verbatim on request items.
	ReasoningSignature string

	// ToolCallID joins KindToolCall and KindToolResult items together.
	ToolCallID string

	// ToolName + ToolArgs describe a function-call invocation
	// (KindToolCall). Arguments is the raw JSON string the model
	// produced; the dispatcher round-trips it as-is.
	ToolName string
	ToolArgs string

	// ToolResult is the tool's response, populated for KindToolResult.
	ToolResult string
}

// InputKind discriminates InputItem.
type InputKind string

const (
	// KindMessage is a user/assistant/system message with plain text.
	KindMessage InputKind = "message"
	// KindReasoning is a prior-turn chain-of-thought block. Adapters
	// echo this on the upstream side so the model keeps its context.
	KindReasoning InputKind = "reasoning"
	// KindToolCall is a function-call invocation by the assistant.
	KindToolCall InputKind = "tool_call"
	// KindToolResult is the result the tool returned to the assistant.
	KindToolResult InputKind = "tool_result"
)

// Tool describes one function-tool the model may invoke. Parameters is
// a JSON-Schema object describing the expected arguments.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolCall is one function-call invocation in a Response.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON string from the model
}

// Response is the typed result of a Provider.Respond call.
//
// Text + ToolCalls + Reasoning together describe the assistant's
// output for this turn. The handler stitches these into the public
// Responses-API JSON shape on the wire.
//
// Raw is kept for audit / debugging — the unmodified upstream body the
// adapter parsed. The proxy never logs Raw; it ends up in the
// llm_usage_log row only if a future audit path explicitly pulls it.
type Response struct {
	ID                 string
	Text               string
	Reasoning          string
	ReasoningSignature string
	ToolCalls          []ToolCall
	Usage              Usage
	Raw                []byte
	StatusCode         int
}

// Usage is the per-request token-accounting block. ReasoningTokens is
// surfaced separately for reasoning models that bill differently for
// the chain-of-thought portion (DeepSeek reasoner, OpenAI o-series);
// upstreams without that breakdown leave it zero.
type Usage struct {
	PromptTokens     int32
	CompletionTokens int32
	ReasoningTokens  int32
	TotalTokens      int32
}

// UpstreamError wraps a non-2xx upstream response so the handler can
// surface the upstream status + message faithfully rather than
// collapsing every upstream 4xx to a generic 502.
type UpstreamError struct {
	StatusCode int
	Message    string
	Raw        []byte
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream %d: %s", e.StatusCode, e.Message)
}

// Sentinel errors adapters return to signal specific HTTP statuses.
// The handler maps each to a known status code; any other error
// becomes 502 via dispatchStatusFor.
var (
	// ErrStreamingUnsupported is returned when a caller requests
	// `stream: true`. v2 of the typed-adapter interface drops streaming
	// entirely — a typed Response can't represent a partial token
	// stream. Maps to 501.
	ErrStreamingUnsupported = errors.New("streaming not supported by typed-adapter interface")

	// ErrBaseURLRequired is returned when a provider row is missing a
	// required base_url. Maps to 500 because the provider was admin-
	// registered incomplete — caller can't fix it.
	ErrBaseURLRequired = errors.New("provider base_url is required")
)

// Registry maps domain.ProviderType to a Provider adapter. NewRegistry
// panics on duplicate registration — programmer error surfaced loudly.
type Registry struct {
	byType map[domain.ProviderType]Provider
}

func NewRegistry(ps ...Provider) *Registry {
	m := map[domain.ProviderType]Provider{}
	for _, p := range ps {
		t := p.Type()
		if _, dup := m[t]; dup {
			panic("upstream: duplicate provider for type " + string(t))
		}
		m[t] = p
	}
	return &Registry{byType: m}
}

// Lookup returns the adapter registered for t, or (nil, false).
// Callers turn the false return into a 501 — "we don't know how to
// talk to that upstream" is distinct from "the upstream itself
// returned an error".
func (r *Registry) Lookup(t domain.ProviderType) (Provider, bool) {
	p, ok := r.byType[t]
	return p, ok
}

// Default returns a Registry populated with the shipped adapters.
func Default() *Registry {
	return NewRegistry(
		NewOpenAI(),
		NewOpenAICompat(),
		NewAnthropic(),
	)
}
