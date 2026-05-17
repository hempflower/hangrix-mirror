// Package tools owns the merged tool catalogue the agent exposes to
// the LLM: in-process locals (read / write / edit / glob / grep / bash
// / webfetch) plus the platform tools (issue_read, issue_diff,
// issue_comment, …) which are hardcoded built-ins talking HTTP to
// `<HANGRIX_PLATFORM_BASE_URL>/api/agent/tools/<name>`.
//
// HANGRIX_TOOL_CATALOG is parsed by the caller (cmd/hangrix-agent) and
// passed in as Allow. An empty Allow means "no filter — every tool is
// visible"; a non-empty Allow restricts the catalogue to listed names
// (unknown names are dropped silently). The filter applies to *both*
// local and platform tools so a role can shut off `bash` or
// `issue_merge` symmetrically.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

// Registry is the single dispatch point. Built once at startup;
// goroutine safe because every backing tool's Call is.
type Registry struct {
	descriptors      []llm.ToolDescriptor
	byName           map[string]local.Tool
	platformByName   map[string]struct{} // set of names sourced from platform
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
// is local first then platform; within each group the supplied order
// is preserved.
func Build(localTools, platformTools []local.Tool, allow []string) *Registry {
	allowSet := buildAllowSet(allow)
	r := &Registry{
		byName:         map[string]local.Tool{},
		platformByName: map[string]struct{}{},
	}
	for _, t := range localTools {
		if !allowSet.permit(t.Name()) {
			continue
		}
		r.byName[t.Name()] = t
		r.descriptors = append(r.descriptors, llm.ToolDescriptor{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	for _, t := range platformTools {
		if !allowSet.permit(t.Name()) {
			continue
		}
		r.byName[t.Name()] = t
		r.platformByName[t.Name()] = struct{}{}
		r.descriptors = append(r.descriptors, llm.ToolDescriptor{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
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
	// Platform tools return a structured `{is_error, text}` envelope on
	// soft failure (see internal/tools/platform/platform.go). Project
	// the IsError flag onto our CallResult so the runtime can mark the
	// tool_call frame; the body itself still rides in ResultJSON so the
	// LLM sees the explanation verbatim.
	if m, ok := value.(map[string]any); ok {
		if flag, has := m["is_error"].(bool); has && flag {
			raw, mErr := json.Marshal(value)
			if mErr != nil {
				return CallResult{Source: src, IsError: true, ErrMsg: fmt.Sprintf("marshal result: %s", mErr)}
			}
			text, _ := m["text"].(string)
			return CallResult{Source: src, ResultJSON: raw, IsError: true, ErrMsg: text}
		}
	}
	raw, mErr := json.Marshal(value)
	if mErr != nil {
		return CallResult{Source: src, IsError: true, ErrMsg: fmt.Sprintf("marshal result: %s", mErr)}
	}
	return CallResult{Source: src, ResultJSON: raw}
}

func (r *Registry) knownNames() []string {
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	return names
}

// allowSet wraps "no filter" vs "explicit list" so call sites don't
// have to special-case empty everywhere.
type allowSet struct {
	none  bool
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
var ErrToolNotAllowed = errors.New("tool not allowed")
