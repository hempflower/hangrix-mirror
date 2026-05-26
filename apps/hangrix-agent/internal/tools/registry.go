// Package tools owns the merged tool catalogue the agent exposes to
// the LLM: in-process locals (read / write / edit / glob / grep / bash
// / webfetch) plus the platform tools (issue_read,
// issue_comment, …) which are hardcoded built-ins talking HTTP to
// `<HANGRIX_PLATFORM_BASE_URL>/api/v1/...` REST endpoints, plus any MCP
// tools wired in from configured MCP servers.
//
// Filtering follows a PLATFORM-TOOL WHITELIST model. Local tools and MCP
// tools are ALWAYS registered — the whitelist never touches them. Only
// platform tools are gated: HANGRIX_PLATFORM_TOOLS is parsed by the
// caller into a slice of shell-style glob patterns and passed in as
// platformAllow. A platform tool is registered only if its name matches
// at least one pattern (exact match or Go path.Match — tool names carry
// no `/` so `*` matches the whole name). An empty/nil platformAllow
// registers NO platform tools (strict whitelist). This composes with the
// read/write hiding performed earlier by platform.All: a read-only role
// has already dropped the mutating platform tools before they reach the
// whitelist.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
)

// Source distinguishes a local in-process tool from a remote platform
// one. We surface this in CallResult so the IPC tool_call frame can
// carry it for audit — the runner needs to know "this commit was
// driven by a platform-side issue_merge call, not by a `git push`
// from bash" to classify the audit event correctly.
type Source string

const (
	SourceLocal    Source = "local"
	SourcePlatform Source = "platform"
	SourceMCP      Source = "mcp"
)

// Registry is the single dispatch point. Built once at startup;
// goroutine safe because every backing tool's Call is.
type Registry struct {
	descriptors    []llm.ToolDescriptor
	byName         map[string]local.Tool
	platformByName map[string]struct{} // set of names sourced from platform
	mcpByName      map[string]struct{} // set of names sourced from MCP servers
}

// CallResult is what the runtime persists. ResultJSON is what gets fed
// back to the LLM as the function-call output; Source + IsError +
// ErrMsg are agent-side metadata.
type CallResult struct {
	Source     Source
	ResultJSON json.RawMessage
	IsError    bool
	ErrMsg     string
}

// Build assembles the registry. platformTools may be empty (no
// platform connection — useful in offline tests). The catalogue order
// is local first then platform then MCP; within each group the supplied
// order is preserved.
//
// Local and MCP tools are always registered. Platform tools are gated by
// platformAllow: a platform tool is registered only if its name matches
// at least one glob pattern (see matchPlatformAllow). An empty/nil
// platformAllow registers NO platform tools.
func Build(localTools, platformTools, mcpTools []local.Tool, platformAllow []string) *Registry {
	r := &Registry{
		byName:         map[string]local.Tool{},
		platformByName: map[string]struct{}{},
		mcpByName:      map[string]struct{}{},
	}
	register := func(tools []local.Tool, gate func(string) bool, mark func(string)) {
		for _, t := range tools {
			if gate != nil && !gate(t.Name()) {
				continue
			}
			r.byName[t.Name()] = t
			if mark != nil {
				mark(t.Name())
			}
			r.descriptors = append(r.descriptors, llm.ToolDescriptor{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Schema(),
			})
		}
	}
	register(localTools, nil, nil)
	register(platformTools, func(n string) bool { return matchPlatformAllow(platformAllow, n) }, func(n string) { r.platformByName[n] = struct{}{} })
	register(mcpTools, nil, func(n string) { r.mcpByName[n] = struct{}{} })
	return r
}

// Catalog returns the LLM-facing tool descriptors. Returned slice is
// shared with the registry — callers must not mutate it.
func (r *Registry) Catalog() []llm.ToolDescriptor { return r.descriptors }

// Call dispatches one function-call. Unknown tool names map to an
// IsError result with a helpful message — we surface this back to the
// LLM rather than crashing the loop, so the model can self-correct
// (typo, wrong namespace).
func (r *Registry) Call(ctx context.Context, name string, args json.RawMessage) CallResult {
	t, ok := r.byName[name]
	if !ok {
		return CallResult{
			Source:  SourceLocal,
			IsError: true,
			ErrMsg:  fmt.Sprintf("unknown tool %q (available: %s)", name, strings.Join(r.knownNames(), ", ")),
		}
	}
	src := SourceLocal
	if _, isPlatform := r.platformByName[name]; isPlatform {
		src = SourcePlatform
	}
	if _, isMCP := r.mcpByName[name]; isMCP {
		src = SourceMCP
	}
	out, err := t.Call(ctx, args)
	return r.makeResult(src, out, err)
}

func (r *Registry) makeResult(src Source, value any, err error) CallResult {
	if err != nil {
		return CallResult{Source: src, IsError: true, ErrMsg: err.Error()}
	}
	if value == nil {
		return CallResult{Source: src, ResultJSON: json.RawMessage("null")}
	}
	// Platform tools return a structured `{is_error, ...}` envelope on
	// soft failure. The legacy shape is `{is_error, text}`; the v1 REST
	// shape is `{is_error, status, error}`. Project the IsError flag
	// onto our CallResult so the runtime can mark the tool_call frame;
	// the body itself still rides in ResultJSON so the LLM sees the
	// explanation verbatim.
	if m, ok := value.(map[string]any); ok {
		if flag, has := m["is_error"].(bool); has && flag {
			raw, mErr := json.Marshal(value)
			if mErr != nil {
				return CallResult{Source: src, IsError: true, ErrMsg: fmt.Sprintf("marshal result: %s", mErr)}
			}
			text, _ := m["text"].(string)
			if text == "" {
				text, _ = m["error"].(string)
			}
			return CallResult{Source: src, ResultJSON: raw, IsError: true, ErrMsg: text}
		}
	}
	raw, mErr := json.Marshal(value)
	if mErr != nil {
		return CallResult{Source: src, IsError: true, ErrMsg: fmt.Sprintf("marshal result: %s", mErr)}
	}
	// Apply the unified size guard.  When a tool result balloons past
	// the budget (long bash output, a huge webfetch page, …), the
	// guard truncates the in-context payload and writes the full
	// content to a temp file the LLM can read on demand.
	raw = guardResult(raw)
	return CallResult{Source: src, ResultJSON: raw}
}

func (r *Registry) knownNames() []string {
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	return names
}

// matchPlatformAllow reports whether name matches at least one of the
// whitelist glob patterns. Each pattern is tested as an exact string
// match first (fast path) then as a Go path.Match glob. Tool names carry
// no `/`, so `*` and `?` match across the entire name. A malformed
// pattern (path.ErrBadPattern) simply fails to match — it never panics.
// An empty/nil patterns slice matches nothing (strict whitelist).
func matchPlatformAllow(patterns []string, name string) bool {
	for _, p := range patterns {
		if p == name {
			return true
		}
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// ParsePlatformTools parses the HANGRIX_PLATFORM_TOOLS env var into the
// platform-tool whitelist. Empty / unset returns nil (no platform tools).
// Anything non-JSON is an error so a typo in the runner config doesn't
// silently disable every platform tool. The patterns are shell-style
// globs (matched by matchPlatformAllow) applied to platform tools only.
func ParsePlatformTools(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("HANGRIX_PLATFORM_TOOLS: %w", err)
	}
	return out, nil
}

// ErrToolNotAllowed is returned by callers that want to surface a richer
// error than CallResult.IsError (e.g. the runtime emitting a typed log).
var ErrToolNotAllowed = errors.New("tool not allowed")
