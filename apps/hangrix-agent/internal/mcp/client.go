// Package mcp is a minimal HTTP MCP client. It speaks the Streamable HTTP
// transport (single endpoint, JSON-RPC 2.0 over POST, optional SSE for
// notifications) using only stdlib. The agent uses this to discover and
// invoke platform tools (issue.* / roster.*) without pulling in the
// official MCP SDK — the surface we need is small enough to write
// directly, and the dependency cost would dwarf the code.
//
// Wire model:
//   - POST endpoint with {"jsonrpc":"2.0","id":<n>,"method":<m>,"params":<p>}
//   - Bearer-auth header with the session token
//   - Server responds with one JSON object (Content-Type: application/json)
//     OR an SSE stream of events for long-running calls. We treat the
//     non-stream JSON path as the only required one — M6b only exercises
//     `tools/list` and `tools/call`, both of which servers can satisfy
//     with a single JSON response.
//
// Notifications/streaming responses are out of scope here; if a server
// returns Content-Type: text/event-stream we read the first `data:` event
// containing a result and stop. That covers servers that always frame in
// SSE without us needing a full event-loop.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Client speaks to one MCP endpoint with one bearer token.
type Client struct {
	endpoint string
	token    string
	http     *http.Client
	nextID   atomic.Int64
}

// New builds a Client. The endpoint must be the full Streamable HTTP
// endpoint URL (the platform exposes one per session — that mapping
// lives behind HANGRIX_PLATFORM_MCP_ENDPOINT).
func New(endpoint, token string) *Client {
	return &Client{
		endpoint: endpoint,
		token:    token,
		http:     &http.Client{Timeout: 60 * time.Second},
	}
}

// Tool is the JSON-Schema-decorated tool descriptor MCP servers return
// from `tools/list`. We re-export it (rather than aliasing the LLM's
// ToolDescriptor) because the schemas the registry consumes need both the
// MCP source provenance and the original JSON-Schema, and conflating
// types here would force the registry to do that bookkeeping itself.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// CallResult is the response shape from `tools/call`. Per the MCP spec
// `content` is an array of typed parts (text / image / resource); we
// concatenate text parts into Text and surface IsError verbatim.
type CallResult struct {
	Text    string
	IsError bool
	Raw     json.RawMessage // full result payload, for audit
}

// ListTools issues `tools/list`. Returns the catalogue with raw
// JSON-Schemas the registry can hand to the LLM.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := c.call(ctx, "tools/list", nil, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

// CallTool issues `tools/call` with arguments as a raw JSON object. The
// agent passes the LLM-emitted argument string straight through; we wrap
// it in the MCP envelope here.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*CallResult, error) {
	if len(arguments) == 0 {
		// MCP requires an object for arguments even when the tool takes
		// none — empty string from the LLM means "no args", normalise to
		// {}. Skipping this check would surface as a server-side
		// validation error and waste a turn.
		arguments = json.RawMessage(`{}`)
	}
	params := map[string]any{
		"name":      name,
		"arguments": json.RawMessage(arguments),
	}
	var out struct {
		IsError bool              `json:"isError"`
		Content []json.RawMessage `json:"content"`
	}
	raw, err := c.callRaw(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcp: decode tools/call result: %w", err)
	}
	res := &CallResult{IsError: out.IsError, Raw: raw}
	var textBuf strings.Builder
	for _, c := range out.Content {
		var part struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(c, &part); err != nil {
			continue
		}
		if part.Type == "text" {
			textBuf.WriteString(part.Text)
		}
	}
	res.Text = textBuf.String()
	return res, nil
}

// call is the JSON-RPC plumbing. Decodes the result field directly into
// the caller-supplied destination — saves an allocation versus going via
// callRaw + json.Unmarshal at every call site.
func (c *Client) call(ctx context.Context, method string, params any, dst any) error {
	raw, err := c.callRaw(ctx, method, params)
	if err != nil {
		return err
	}
	if dst == nil {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("mcp: decode %s result: %w", method, err)
	}
	return nil
}

// callRaw issues one JSON-RPC request with retry. Same backoff shape as
// the LLM client — these are sibling outbound calls from a long-lived
// agent process and benefit from the same policy.
func (c *Client) callRaw(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	envelope := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		envelope["params"] = params
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal request: %w", err)
	}

	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
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
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		raw, terminal, err := readJSONRPCResponse(resp)
		if err != nil {
			lastErr = err
			if terminal {
				return nil, err
			}
			continue
		}
		// Decode the JSON-RPC envelope; surface any error field as Go error.
		var rpc struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int             `json:"code"`
				Message string          `json:"message"`
				Data    json.RawMessage `json:"data"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &rpc); err != nil {
			return nil, fmt.Errorf("mcp: decode envelope: %w", err)
		}
		if rpc.Error != nil {
			return nil, fmt.Errorf("mcp: %s rpc error %d: %s", method, rpc.Error.Code, rpc.Error.Message)
		}
		if rpc.Result == nil {
			// MCP allows an empty result for some methods (notifications)
			// but tools/list and tools/call always return one — treat
			// empty as a bug in the upstream rather than retry.
			return nil, fmt.Errorf("mcp: %s returned empty result", method)
		}
		return rpc.Result, nil
	}
	if lastErr == nil {
		lastErr = errors.New("mcp: exhausted retries")
	}
	return nil, lastErr
}

func (c *Client) do(ctx context.Context, body []byte) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Content-Type", "application/json")
	// The Streamable HTTP transport spec requires clients to advertise
	// both content types so the server can choose between a one-shot JSON
	// reply and an SSE stream.
	r.Header.Set("Accept", "application/json, text/event-stream")
	return c.http.Do(r)
}

// readJSONRPCResponse normalises the two server response shapes
// (single-JSON or SSE) into a single byte slice carrying the JSON-RPC
// envelope. Returns terminal=true when the error is a 4xx (no point
// retrying); 5xx and transport errors are non-terminal.
func readJSONRPCResponse(resp *http.Response) (raw []byte, terminal bool, err error) {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// 4xx is a request bug — schema mismatch, missing tool, auth fail.
		// Don't retry; let the caller see the message.
		terminal = resp.StatusCode < 500
		return nil, terminal, fmt.Errorf("mcp: http %d: %s", resp.StatusCode, string(body))
	}
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		raw, err = io.ReadAll(resp.Body)
		return raw, false, err
	}
	if strings.HasPrefix(ct, "text/event-stream") {
		// Read one `data:` line containing the JSON-RPC response object.
		// We deliberately don't run a full SSE event loop — tools/call
		// resolves to one final response event in this minimal client.
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
		var buf bytes.Buffer
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if buf.Len() > 0 {
					return buf.Bytes(), false, nil
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				buf.WriteString(strings.TrimPrefix(line, "data:"))
				buf.WriteByte('\n')
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, false, err
		}
		if buf.Len() == 0 {
			return nil, false, errors.New("mcp: SSE stream ended without data")
		}
		return buf.Bytes(), false, nil
	}
	// Unknown content type — try JSON anyway; a misbehaving server is more
	// useful with a parse error than a content-type-error.
	raw, err = io.ReadAll(resp.Body)
	return raw, false, err
}
