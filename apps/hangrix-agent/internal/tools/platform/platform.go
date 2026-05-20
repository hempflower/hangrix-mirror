// Package platform implements the agent-side "platform tools" as
// built-ins. Each tool is a thin HTTP shim that POSTs the LLM-emitted
// argument JSON to the platform's REST endpoint and returns whatever
// the server gives back.
//
// We used to discover these over an MCP `tools/list` round-trip; that
// extra protocol layer is gone. The agent now ships hardcoded
// descriptors (name + description + JSON-Schema) that must match the
// server's service-layer definitions. A drift between the two surfaces
// as a 400 / 404 on the first call — not silent.
//
// Wire shape (one endpoint per tool, see
// apps/hangrix/internal/modules/platform_mcp/handler/handler.go):
//
//	POST <base>/<tool-name>
//	Authorization: Bearer hgxs_…
//	Content-Type: application/json
//	<args JSON>
//
//	200 { "is_error": false, "text": "<server-side payload>" }
//
// Soft errors (ACL denied, validation failure inside the tool) ride on
// is_error=true with the explanation in `text`; the agent surfaces both
// to the LLM verbatim.
package platform

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
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
)

// Client is the shared HTTP backend every platform tool uses. One per
// process; goroutine-safe.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		// httpx.NewClient honours HANGRIX_INSECURE_SKIP_TLS_VERIFY
		// so the platform-tools path uses the same TLS knob as the
		// LLM client when the container's CA store is broken.
		http: httpx.NewClient(60 * time.Second),
	}
}

// callResponse mirrors the server's callResponse JSON shape. is_error
// + text are the only fields; success vs soft-error is the only
// distinction the agent needs to surface.
type callResponse struct {
	IsError bool   `json:"is_error"`
	Text    string `json:"text"`
}

// Call POSTs args to <base>/<name>. The returned string is what the
// LLM sees as the function-call output; isError is forwarded so the
// runtime can flag the tool_call frame appropriately.
//
// Retry policy mirrors the LLM client: same 3-attempt exponential
// backoff for transport errors and 5xx. 4xx is terminal — schema
// mismatch / auth fail don't get better on retry.
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (text string, isError bool, err error) {
	if len(args) == 0 || strings.TrimSpace(string(args)) == "" {
		args = json.RawMessage(`{}`)
	}
	url := c.baseURL + "/" + name

	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(math.Pow(2, float64(attempt-1))*500) * time.Millisecond
			select {
			case <-ctx.Done():
				return "", false, ctx.Err()
			case <-time.After(delay):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(args))
		if err != nil {
			return "", false, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return "", false, ctx.Err()
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("platform tool %q: http %d: %s", name, resp.StatusCode, truncate(body, 256))
			continue
		}
		if resp.StatusCode >= 400 {
			// 4xx is terminal — the request itself is wrong (auth fail,
			// malformed body). Surface verbatim; no retry will help.
			return "", false, fmt.Errorf("platform tool %q: http %d: %s", name, resp.StatusCode, truncate(body, 512))
		}
		var out callResponse
		if err := json.Unmarshal(body, &out); err != nil {
			return "", false, fmt.Errorf("platform tool %q: decode response: %w", name, err)
		}
		return out.Text, out.IsError, nil
	}
	if lastErr == nil {
		lastErr = errors.New("platform tool: exhausted retries")
	}
	return "", false, lastErr
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// Tool wraps a single hardcoded platform tool descriptor (the
// name/description/schema we used to fetch over MCP) as a
// local.Tool. The Call method is the HTTP shim above.
type Tool struct {
	name        string
	description string
	schema      map[string]any
	client      *Client
}

func (t *Tool) Name() string             { return t.name }
func (t *Tool) Description() string      { return t.description }
func (t *Tool) Schema() map[string]any   { return t.schema }

// Call returns the platform's text payload as the result. Soft errors
// (is_error=true) are returned as a structured `{is_error, text}`
// object so the registry can mark the IPC tool_call frame accordingly
// and the LLM still sees the failure reason.
//
// We deliberately do NOT use a Go error for soft failures here — those
// represent "the tool ran, the server said no" rather than "the call
// itself blew up", and the LLM benefits from seeing the response text
// verbatim. Transport / 5xx / 4xx errors collapse to Go errors.
func (t *Tool) Call(ctx context.Context, args json.RawMessage) (any, error) {
	text, isError, err := t.client.Call(ctx, t.name, args)
	if err != nil {
		return nil, err
	}
	if isError {
		return map[string]any{"is_error": true, "text": text}, nil
	}
	// Most tools serialize their result as a JSON string in `text`; if
	// it parses, forward the structured value so the LLM sees nice
	// nested JSON rather than an escaped string. Plain-text payloads
	// (rare — issue_close returns one) fall through as a string.
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		return parsed, nil
	}
	return text, nil
}

// All returns every platform tool the agent ships with, bound to the
// supplied HTTP client. Order matters for catalogue stability — keep
// read-only tools first then mutating ones, mirroring the order the
// MCP server used to declare them.
//
// Schemas duplicate apps/hangrix/internal/modules/platform_mcp/service/
// tools_{read,write}.go. The duplication is bounded (one block per
// tool, ~10 lines each) and the cost — drift surfaces as a 4xx on
// the first real call — is fine for v1. A future "fetch catalogue at
// startup" path could collapse this, but the built-in approach keeps
// the agent boot deterministic and runnable offline (smoke tests).
func All(client *Client) []local.Tool {
	if client == nil {
		return nil
	}
	descriptors := []struct {
		name, description string
		schema            map[string]any
	}{
		{
			name:        "issue_read",
			description: "Read the current issue's metadata, comments, and timeline events.",
			schema:      objectSchema(nil, nil),
		},
		{
			name:        "issue_diff",
			description: "Return the diff between the issue branch and its base branch (file-level unified diff).",
			schema:      objectSchema(nil, nil),
		},
		{
			name:        "issue_mergeable",
			description: "Check whether the issue branch is fast-forward mergeable into its base. Returns mergeable status, mode, and hint.",
			schema:      objectSchema(nil, nil),
		},

		{
			name:        "issue_children",
			description: "List sub-issues (child issues) of the current issue.",
			schema:      objectSchema(nil, nil),
		},
		{
			name:        "issue_checks",
			description: "List the latest state of each CI check on the issue's head commit. Currently always returns [].",
			schema:      objectSchema(nil, nil),
		},
		{
			name:        "roster_list",
			description: "List every active role session on the current issue.",
			schema:      objectSchema(nil, nil),
		},
		{
			name:        "issue_create",
			description: "Create a new issue in the current repo. Set `parent: true` to create as a sub-issue of the current issue.",
			schema: objectSchema(map[string]any{
				"title":  stringProp("Issue title (1-200 characters)."),
				"body":   stringProp("Optional issue body (markdown)."),
				"parent": boolProp("When true, creates the new issue as a sub-issue of the current issue. Default: false."),
			}, []string{"title"}),
		},
		{
			name:        "issue_comment",
			description: "Post a comment on the current issue. `body` is markdown; @agent-<role-key> mentions wake other roles.",
			schema: objectSchema(map[string]any{
				"body":      stringProp("The comment body. Markdown allowed; mentions follow @agent-<role-key> grammar."),
				"file_path": stringProp("Optional path to anchor the comment to a file (inline review). Omit for top-level."),
				"line":      intProp("Optional line number to anchor inline. Requires file_path."),
			}, []string{"body"}),
		},
		{
			name:        "issue_review_vote",
			description: "Cast a structured review vote on the current issue (approve / request_changes / abstain).",
			schema: objectSchema(map[string]any{
				"value": enumProp("Vote outcome.", []string{"approve", "request_changes", "abstain"}),
				"reason": stringProp("Free-text rationale shown on the timeline. Recommended even for 'approve'."),
			}, []string{"value"}),
		},
		{
			name:        "issue_close",
			description: "Close the current issue without merging. Archives every active agent session on it.",
			schema: objectSchema(map[string]any{
				"reason": stringProp("Optional rationale, recorded on the timeline."),
			}, nil),
		},
		{
			name:        "issue_merge",
			description: "Merge the issue branch into its base. Fails if there are no commits or the merge would conflict.",
			schema: objectSchema(map[string]any{
				"message": stringProp("Optional merge commit message. Defaults to 'Merge issue #N: <title>'."),
			}, nil),
		},
		{
			name:        "session_recover",
			description: "Recover a failed / succeeded / cancelled / idle session on the current issue. Sets it back to pending so the runner picks it up. Restricted to sessions on the same issue.",
			schema: objectSchema(map[string]any{
				"session_id": intProp("The session ID to recover. Must be on the same issue as the caller."),
			}, []string{"session_id"}),
		},
		{
			name:        "release_create",
			description: "Create a new release in draft state from an existing git tag. The tag must already exist in the repo.",
			schema: objectSchema(map[string]any{
				"tag_name": stringProp("The existing git tag to create the release from (required)."),
				"title":    stringProp("Optional release title. Defaults to the tag name if omitted."),
				"notes":    stringProp("Optional release notes (markdown)."),
			}, []string{"tag_name"}),
		},
		{
			name:        "release_upload_asset",
			description: "Upload a custom asset file to an existing release. The file is read from the agent's workspace.",
			schema: objectSchema(map[string]any{
				"release_id":   intProp("The release ID to attach the asset to (required)."),
				"name":         stringProp("The filename for the asset (required)."),
				"content_type": stringProp("Optional MIME type. Guessed from the filename extension if omitted."),
				"file_path":    stringProp("Absolute or workspace-relative path to the file to upload (required)."),
			}, []string{"release_id", "name", "file_path"}),
		},
		{
			name:        "release_publish",
			description: "Publish a draft release, making it visible as an official release with a published_at timestamp.",
			schema: objectSchema(map[string]any{
				"release_id": intProp("The release ID to publish (required)."),
			}, []string{"release_id"}),
		},
		{
			name:        "release_update",
			description: "Edit an existing release's metadata (title, notes). The tag_name can only be changed while the release is still a draft.",
			schema: objectSchema(map[string]any{
				"release_id": intProp("The release ID to update (required)."),
				"title":      stringProp("Optional new release title."),
				"notes":      stringProp("Optional new release notes (markdown)."),
				"tag_name":   stringProp("Optional new tag name. Only mutable when the release is still a draft."),
			}, []string{"release_id"}),
		},
		{
			name:        "release_delete",
			description: "Delete a release and all of its custom assets. Derived source archives (zip/tar.gz) are not separately stored and do not need cleanup.",
			schema: objectSchema(map[string]any{
				"release_id": intProp("The release ID to delete (required)."),
			}, []string{"release_id"}),
		},

	}
	out := make([]local.Tool, 0, len(descriptors))
	for _, d := range descriptors {
		out = append(out, &Tool{
			name:        d.name,
			description: d.description,
			schema:      d.schema,
			client:      client,
		})
	}
	return out
}

func objectSchema(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object"}
	if props != nil {
		out["properties"] = props
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func enumProp(desc string, values []string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
