// Package tools is the merge point between local in-process tools and
// remote MCP-served platform tools. It exposes:
//
//   - Catalog: the list of ToolDescriptor handed to the LLM.
//   - Call: dispatches a function call (by name) to either a local Tool
//     or the MCP client, returning a JSON-serialisable result.
//
// HANGRIX_TOOL_CATALOG is parsed by the caller (cmd/hangrix-agent) and
// passed in as Allow. An empty Allow means "no filter — every tool is
// visible"; a non-empty Allow restricts the catalogue to listed names
// (unknown names are dropped silently). The filter applies to *both*
// local and MCP-served tools so a role can shut off `bash` or
// `issue.merge` symmetrically.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/mcp"
	"github.com/hangrix/hangrix/apps/hangrix-agent/pkg/tools/local"
)

// Source distinguishes a local in-process tool from a remote MCP one.
// We surface this in CallResult so the IPC tool_call frame can carry it
// for audit — the runner needs to know "this commit was driven by a
// platform-side issue.merge call, not by a `git push` from bash" to
// classify the audit event correctly.
type Source string

const (
	SourceLocal Source = "local"
	SourceMCP   Source = "mcp"
)

// Registry is the single dispatch point. Built once at startup; goroutine
// safe because both local Tool.Call and mcp.Client.CallTool are safe to
// invoke concurrently.
type Registry struct {
	descriptors []llm.ToolDescriptor
	localByName map[string]local.Tool
	mcpByName   map[string]mcp.Tool
	mcpClient   *mcp.Client
}

// CallResult is what the runtime persists. ResultJSON is what gets fed
// back to the LLM as the function-call output; Source + IsError + ErrMsg
// are agent-side metadata.
type CallResult struct {
	Source     Source
	ResultJSON json.RawMessage
	IsError    bool
	ErrMsg     string
}

// Build assembles the registry. localTools is typically the output of
// local.All(); mcpClient may be nil (no platform connection — the agent
// runs without remote tools, useful in M6b smoke tests).
//
// The order of the catalogue is local first then MCP (in MCP server's
// declared order); within each group, the original order is preserved.
// Stable order matters because the LLM has a small bias toward earlier
// tools in long catalogues.
func Build(ctx context.Context, localTools []local.Tool, mcpClient *mcp.Client, allow []string) (*Registry, error) {
	allowSet := buildAllowSet(allow)
	r := &Registry{
		localByName: map[string]local.Tool{},
		mcpByName:   map[string]mcp.Tool{},
		mcpClient:   mcpClient,
	}
	for _, t := range localTools {
		if !allowSet.permit(t.Name()) {
			continue
		}
		r.localByName[t.Name()] = t
		r.descriptors = append(r.descriptors, llm.ToolDescriptor{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	if mcpClient != nil {
		mcpTools, err := mcpClient.ListTools(ctx)
		if err != nil {
			return nil, fmt.Errorf("tools: list remote: %w", err)
		}
		for _, t := range mcpTools {
			if !allowSet.permit(t.Name) {
				continue
			}
			r.mcpByName[t.Name] = t
			schema := t.InputSchema
			if schema == nil {
				// LLM strictly requires a schema; fall back to "object with
				// any properties" so a poorly-described upstream tool is
				// still callable, just under-validated.
				schema = map[string]any{"type": "object"}
			}
			r.descriptors = append(r.descriptors, llm.ToolDescriptor{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schema,
			})
		}
	}
	return r, nil
}

// Catalog returns the LLM-facing tool descriptors. Returned slice is
// shared with the registry — callers must not mutate it.
func (r *Registry) Catalog() []llm.ToolDescriptor { return r.descriptors }

// Call dispatches one function-call. Unknown tool names map to an
// IsError result with a helpful message — we surface this back to the
// LLM rather than crashing the loop, so the model can self-correct
// (typo, wrong namespace).
func (r *Registry) Call(ctx context.Context, name string, args json.RawMessage) CallResult {
	if t, ok := r.localByName[name]; ok {
		out, err := t.Call(ctx, args)
		return r.makeResult(SourceLocal, out, err)
	}
	if _, ok := r.mcpByName[name]; ok {
		if r.mcpClient == nil {
			return CallResult{Source: SourceMCP, IsError: true, ErrMsg: "remote tool advertised but no MCP client configured"}
		}
		res, err := r.mcpClient.CallTool(ctx, name, args)
		if err != nil {
			return r.makeResult(SourceMCP, nil, err)
		}
		// MCP tools may return text or structured payloads; we forward
		// the raw `result` envelope so the LLM sees exactly what the
		// platform returned. IsError reflects the MCP-level signal.
		return CallResult{
			Source:     SourceMCP,
			ResultJSON: res.Raw,
			IsError:    res.IsError,
			ErrMsg:     "",
		}
	}
	available := r.knownNames()
	return CallResult{
		Source:  SourceLocal, // not really, but the caller doesn't need fidelity here
		IsError: true,
		ErrMsg:  fmt.Sprintf("unknown tool %q (available: %s)", name, strings.Join(available, ", ")),
	}
}

func (r *Registry) makeResult(src Source, value any, err error) CallResult {
	if err != nil {
		return CallResult{Source: src, IsError: true, ErrMsg: err.Error()}
	}
	if value == nil {
		return CallResult{Source: src, ResultJSON: json.RawMessage("null")}
	}
	raw, mErr := json.Marshal(value)
	if mErr != nil {
		return CallResult{Source: src, IsError: true, ErrMsg: fmt.Sprintf("marshal result: %s", mErr)}
	}
	return CallResult{Source: src, ResultJSON: raw}
}

func (r *Registry) knownNames() []string {
	names := make([]string, 0, len(r.localByName)+len(r.mcpByName))
	for n := range r.localByName {
		names = append(names, n)
	}
	for n := range r.mcpByName {
		names = append(names, n)
	}
	return names
}

// allowSet wraps "no filter" vs "explicit list" so call sites don't have
// to special-case empty everywhere.
type allowSet struct {
	none  bool // true means "no filter, allow everything"
	names map[string]struct{}
}

func buildAllowSet(allow []string) allowSet {
	if len(allow) == 0 {
		return allowSet{none: true}
	}
	s := allowSet{names: make(map[string]struct{}, len(allow))}
	for _, n := range allow {
		s.names[n] = struct{}{}
	}
	return s
}

func (s allowSet) permit(name string) bool {
	if s.none {
		return true
	}
	_, ok := s.names[name]
	return ok
}

// ParseToolCatalog parses the HANGRIX_TOOL_CATALOG env var. Empty / unset
// returns nil (no filter). Anything non-JSON is an error so a typo in the
// runner config doesn't silently disable the filter.
func ParseToolCatalog(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("HANGRIX_TOOL_CATALOG: %w", err)
	}
	return out, nil
}

// ErrToolNotAllowed is returned by callers that want to surface a richer
// error than CallResult.IsError (e.g. the runtime emitting a typed log).
// Kept here so package boundaries don't proliferate it.
var ErrToolNotAllowed = errors.New("tool not allowed")
