package upstream

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The Responses-API wire format has two union-typed fields that no
// plain Go struct can model:
//
//   - top-level `input`: either a bare string (shorthand for one user
//     message) or an array of typed items.
//   - per-item `content` on message items: either a bare string or an
//     array of typed content blocks.
//
// Both unions get their own typed wrapper below with a UnmarshalJSON
// that hides the variance, so ParseResponsesAPIRequest can read every
// other field through ordinary json tags without any RawMessage hops.

// responsesItems is the decoded `input` value. The custom unmarshaller
// expands the string-shorthand into a single user-message item so the
// rest of the parser only ever iterates a slice.
type responsesItems []responsesItem

func (r *responsesItems) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	// String shorthand → one synthetic user message.
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s != "" {
			*r = responsesItems{{
				Type:    "message",
				Role:    "user",
				Content: responsesContent{Text: s},
			}}
		}
		return nil
	}
	var arr []responsesItem
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf("input must be string or array")
	}
	*r = arr
	return nil
}

// responsesItem captures every field a Responses-API input item may
// carry. Type is the discriminator; per-kind fields are zero-valued on
// items of the other kinds, which is fine because no field's zero
// value collides with a meaningful value of the same kind. Unknown
// item types simply land here with Type set and everything else empty
// — the consumer drops them.
type responsesItem struct {
	Type             string             `json:"type,omitempty"`
	Role             string             `json:"role,omitempty"`
	Content          responsesContent   `json:"content"`
	CallID           string             `json:"call_id,omitempty"`
	Name             string             `json:"name,omitempty"`
	Arguments        string             `json:"arguments,omitempty"`
	Output           string             `json:"output,omitempty"`
	Summary          []responsesSummary `json:"summary,omitempty"`
	EncryptedContent string             `json:"encrypted_content,omitempty"`
}

// responsesContent is the union on a message item's `content` field.
// Text is the concatenation of every text-bearing block (text /
// input_text / output_text) regardless of which wire form was used;
// non-text blocks (images, attachments) are silently dropped per the
// M6a scope. Image / multimodal support gets its own field on this
// struct when we need it.
type responsesContent struct {
	Text string
}

func (c *responsesContent) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		c.Text = s
		return nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &blocks); err != nil {
		// Forward-compatible: an unrecognised shape leaves Text empty
		// rather than failing the whole request. Strict validation
		// belongs to the upstream we forward to.
		return nil
	}
	var out strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "", "text", "input_text", "output_text":
			out.WriteString(b.Text)
		}
	}
	c.Text = out.String()
	return nil
}

// responsesSummary is one entry in a reasoning item's summary array.
// Always typed: no union here.
type responsesSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// responsesTool is one entry in the top-level tools array. We only
// recognise "function" tools today; future tool kinds (file_search,
// computer_use, …) get their own struct.
type responsesTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// responsesReasoning is the OpenAI `reasoning` object. effort is the
// only field we surface today; future knobs (effort_tokens, summary)
// add fields here.
type responsesReasoning struct {
	Effort string `json:"effort"`
}

// ParseResponsesAPIRequest reads an OpenAI Responses-API request body
// and returns a structured Request. Connection params (APIKey, BaseURL,
// Client) are filled in by the handler after this returns. The bool
// return signals whether the caller asked for streaming so the handler
// can 501 before dispatch — adapters never see a streaming Request.
func ParseResponsesAPIRequest(body []byte) (*Request, bool, error) {
	var wire struct {
		Model           string             `json:"model"`
		Instructions    string             `json:"instructions"`
		Input           responsesItems     `json:"input"`
		Tools           []responsesTool    `json:"tools"`
		MaxOutputTokens int                `json:"max_output_tokens"`
		Temperature     *float64           `json:"temperature"`
		Reasoning       responsesReasoning `json:"reasoning"`
		Stream          bool               `json:"stream"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &wire); err != nil {
			return nil, false, fmt.Errorf("decode responses request: %w", err)
		}
	}

	req := &Request{
		Model:           wire.Model,
		Instructions:    wire.Instructions,
		MaxOutputTokens: wire.MaxOutputTokens,
		Temperature:     wire.Temperature,
		ReasoningEffort: wire.Reasoning.Effort,
	}
	for _, t := range wire.Tools {
		if t.Type != "" && t.Type != "function" {
			continue
		}
		req.Tools = append(req.Tools, Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	req.Input = make([]InputItem, 0, len(wire.Input))
	for _, item := range wire.Input {
		req.Input = append(req.Input, itemFromWire(item)...)
	}
	if len(req.Input) == 0 {
		req.Input = nil
	}
	return req, wire.Stream, nil
}

// itemFromWire maps one wire-level responsesItem into zero or one
// InputItem. Unknown types return nil — forward compatibility matters
// more than strict validation. Returns a slice so future item shapes
// that expand to multiple InputItems (e.g. multimodal content split
// into text + image items) can do so without restructuring callers.
func itemFromWire(w responsesItem) []InputItem {
	switch w.Type {
	case "", "message":
		role := w.Role
		if role == "" {
			role = "user"
		}
		return []InputItem{{Kind: KindMessage, Role: role, Text: w.Content.Text}}
	case "function_call":
		return []InputItem{{Kind: KindToolCall, ToolCallID: w.CallID, ToolName: w.Name, ToolArgs: w.Arguments}}
	case "function_call_output":
		return []InputItem{{Kind: KindToolResult, ToolCallID: w.CallID, ToolResult: w.Output}}
	case "reasoning":
		var txt strings.Builder
		for _, s := range w.Summary {
			if s.Type == "" || s.Type == "summary_text" {
				txt.WriteString(s.Text)
			}
		}
		return []InputItem{{Kind: KindReasoning, Reasoning: txt.String(), ReasoningSignature: w.EncryptedContent}}
	}
	return nil
}

// responsesResponse / responsesOutputItem / etc. are the typed
// counterparts to the read-side wrappers above — the shapes the public
// Responses-API wire expects on the response side. Defined as separate
// types from the request-side responsesItem because the field rules
// are subtly different (e.g. message content is always an array of
// blocks on output, never the string shorthand) and conflating them
// would force a custom MarshalJSON to disambiguate.
type responsesResponse struct {
	ID        string                `json:"id"`
	Object    string                `json:"object"`
	CreatedAt int64                 `json:"created_at"`
	Status    string                `json:"status"`
	Output    []responsesOutputItem `json:"output"`
	Usage     responsesUsage        `json:"usage"`
}

// responsesOutputItem covers all three output kinds (reasoning,
// message, function_call). Per-kind fields use omitempty so the wire
// only carries what's relevant; Type is the discriminator and is
// always present.
type responsesOutputItem struct {
	Type             string                `json:"type"`
	ID               string                `json:"id,omitempty"`
	Role             string                `json:"role,omitempty"`
	Content          []responsesContentOut `json:"content,omitempty"`
	Summary          []responsesSummary    `json:"summary,omitempty"`
	EncryptedContent string                `json:"encrypted_content,omitempty"`
	CallID           string                `json:"call_id,omitempty"`
	Name             string                `json:"name,omitempty"`
	Arguments        string                `json:"arguments,omitempty"`
}

// responsesContentOut is a single output-side content block. Always
// `type: "output_text"` today; multimodal output goes here when we
// support it.
type responsesContentOut struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// responsesUsage is the per-response token-accounting block. The
// pointer on OutputDetails keeps the field absent when we have nothing
// to report — OpenAI nests reasoning_tokens under output_tokens_details
// only on the o-series, so emitting an empty object for non-reasoning
// models would mislead anyone parsing the wire.
type responsesUsage struct {
	InputTokens   int32                         `json:"input_tokens"`
	OutputTokens  int32                         `json:"output_tokens"`
	TotalTokens   int32                         `json:"total_tokens"`
	OutputDetails *responsesOutputTokensDetails `json:"output_tokens_details,omitempty"`
}

type responsesOutputTokensDetails struct {
	ReasoningTokens int32 `json:"reasoning_tokens"`
}

// MarshalResponsesAPIResponse renders a typed Response as JSON in the
// OpenAI Responses-API shape. The handler writes the result directly
// onto the wire — callers see whatever the spec says regardless of
// which adapter actually fielded the call.
//
// Output ordering follows the spec: reasoning items first, then the
// assistant message, then any function-call items. A streaming caller
// would see thinking arrive before the answer; we preserve the same
// order for buffered callers so their parsing code is symmetric.
func MarshalResponsesAPIResponse(r *Response) ([]byte, error) {
	body := responsesResponse{
		ID:        r.ID,
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Status:    "completed",
		Usage: responsesUsage{
			InputTokens:  r.Usage.PromptTokens,
			OutputTokens: r.Usage.CompletionTokens,
			TotalTokens:  r.Usage.TotalTokens,
		},
	}
	if r.Usage.ReasoningTokens > 0 {
		body.Usage.OutputDetails = &responsesOutputTokensDetails{
			ReasoningTokens: r.Usage.ReasoningTokens,
		}
	}
	if r.Reasoning != "" {
		body.Output = append(body.Output, responsesOutputItem{
			Type:             "reasoning",
			ID:               "rs_" + r.ID,
			Summary:          []responsesSummary{{Type: "summary_text", Text: r.Reasoning}},
			EncryptedContent: r.ReasoningSignature,
		})
	}
	if r.Text != "" {
		body.Output = append(body.Output, responsesOutputItem{
			Type:    "message",
			Role:    "assistant",
			Content: []responsesContentOut{{Type: "output_text", Text: r.Text}},
		})
	}
	for _, tc := range r.ToolCalls {
		body.Output = append(body.Output, responsesOutputItem{
			Type:      "function_call",
			CallID:    tc.ID,
			Name:      tc.Name,
			Arguments: tc.Arguments,
		})
	}
	return json.Marshal(body)
}
