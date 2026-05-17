// Package domain declares the platform-MCP contract: the Tool interface
// every M7b platform tool implements, the dispatch envelopes the handler
// uses, and the cross-module dependencies tool implementations consume.
//
// The platform_mcp module sits above the issue / repo / runner / git
// modules. It does NOT own its own persistence — every tool calls into
// existing domain interfaces. The split is deliberate: when the same
// "merge an issue" action is reachable both from the web UI (issue
// handler) and the agent (issue_merge tool), only one piece of code
// should do the work.
package domain

import (
	"context"
	"encoding/json"

	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// Tool is one M7b platform-side capability exposed over the MCP server.
// The handler discovers tools through the slice ToolProvider returns;
// per-role filtering applies on top via the session's role_config
// snapshot.
type Tool struct {
	// Name is the wire identifier — `issue_read`, `issue_merge`, etc.
	// Must match the names host yaml authors use in role.can[].
	Name string

	// Description is the LLM-facing tool description. Goes into the
	// MCP `tools/list` response. Should explain *what* the tool does
	// and *when* to use it; the input schema captures *how*.
	Description string

	// InputSchema is a JSON-Schema object describing the tool's args.
	// MCP servers MUST return one; the agent's LLM client uses it to
	// validate the args before dispatch.
	InputSchema map[string]any

	// Call executes the tool with the caller's session row as
	// context. Args is the LLM-emitted argument JSON object — empty
	// `{}` accepted. The returned text becomes the `content[0].text`
	// of the MCP CallTool response.
	Call func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (Result, error)
}

// Result is what a tool emits. Text is the textual representation
// returned to the LLM; the wire envelope wraps it as
// `[{type:"text", text:Text}]`. IsError surfaces "the tool ran but the
// outcome was a structured failure" — distinct from a Go-level error
// which collapses the whole call.
//
// Tools that produce structured data (issue_diff, issue_read) marshal
// their data into Text as JSON; the LLM is the consumer and is happy to
// parse it. We keep the wire shape uniform so the agent's MCP client
// doesn't need to branch.
type Result struct {
	Text    string
	IsError bool
}

// ToolProvider is the seam between the platform_mcp service layer and
// the handler. Every tool registers as one *Tool via the ioc container's
// []ToolProvider slice dependency. Adding a new tool = one new constructor
// returning *Tool, bound `.ToInterface(new(domain.ToolProvider))`.
//
// We use a slice-of-interfaces rather than a registry singleton so the
// MCP handler can be instantiated without knowing which modules
// contribute which tools — the order is whatever ioc resolves and the
// handler sorts deterministically.
type ToolProvider interface {
	PlatformTool() *Tool
}
