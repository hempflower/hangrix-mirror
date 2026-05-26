package tools

import (
	"fmt"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/mcp"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/platform"
	"github.com/hangrix/hangrix/pkg/ioc"
)

type Deps struct {
	Cfg       *config.Config
	LLMClient *llm.Client
}

// LocalBundle pairs the local tool slice with the async lifecycle handle
// the runtime needs for notifications, idle reporting, and shutdown
// cleanup. We provide it as its own ioc node so two consumers — the
// Registry (which only wants Tools) and the runtime.Loop (which only
// wants Async) — can both depend on the same instance without the
// runtime having to fish it out of a generic Tool slice.
type LocalBundle struct {
	Tools []local.Tool
	Async local.AsyncLifecycle
}

func NewLocalBundle(deps *Deps) *LocalBundle {
	b := local.BuildWithResearch(deps.LLMClient, deps.Cfg.Model)
	return &LocalBundle{Tools: b.Tools, Async: b.Async}
}

// RegistryDeps is the dependency set for NewRegistry. It's split off
// from Deps so the ioc graph can resolve Bundle (which NewLocalBundle
// produces from a *Deps) without creating a circular reference. ioc
// constructors take exactly one parameter — a pointer to a Deps-style
// struct whose fields are themselves ioc-provided — so we can't just
// list (Deps, Bundle) as two parameters; the bundle has to be a field
// on its own deps struct.
type RegistryDeps struct {
	Cfg       *config.Config
	Bundle    *LocalBundle
	MCPBundle *mcp.Bundle
}

// AsyncLifecycleDeps wraps the bundle so NewAsyncLifecycle can be a
// single-parameter constructor in the ioc style. The accessor is
// trivial (return Bundle.Async) but having a real ioc node for it lets
// the runtime module depend on local.AsyncLifecycle directly without
// reaching into the LocalBundle struct shape.
type AsyncLifecycleDeps struct {
	Bundle *LocalBundle
}

func NewAsyncLifecycle(deps *AsyncLifecycleDeps) local.AsyncLifecycle { return deps.Bundle.Async }

// NewRegistry parses HANGRIX_TOOL_CATALOG and builds the merged
// (local + platform) registry. Both steps happen exactly once at init
// time; once built the registry is read-only and safe to share across
// the runtime loop's concurrent tool calls.
//
// ParseToolCatalog can fail (malformed allowlist). ioc constructors
// can't return errors, so we panic and let main.go's recover translate.
//
// The platform tools are wired conditionally on PlatformBaseURL: if
// the agent is running with no platform connection (offline / unit
// tests) only the local tools surface.
//
// The local set comes from NewLocalBundle (which calls
// local.BuildWithResearch). NewRegistry consumes the same bundle so
// that the tool instances the registry serves and the lifecycle
// handle the runtime drains are guaranteed to be one-and-the-same.
func NewRegistry(deps *RegistryDeps) *Registry {
	allow, err := ParseToolCatalog(deps.Cfg.ToolCatalog)
	if err != nil {
		panic(fmt.Errorf("tools: parse catalog: %w", err))
	}
	deny, err := ParseToolDeny(deps.Cfg.ToolDeny)
	if err != nil {
		panic(fmt.Errorf("tools: parse deny: %w", err))
	}
	var platformTools []local.Tool
	if base := deps.Cfg.PlatformV1BaseURL(); base != "" {
		client := platform.NewClient(base, deps.Cfg.SessionToken)
		platformTools = platform.All(client, deps.Cfg.RepoPermission == "read")
	}
	var mcpTools []local.Tool
	if deps.MCPBundle != nil {
		mcpTools = deps.MCPBundle.Tools
	}
	return Build(deps.Bundle.Tools, platformTools, mcpTools, allow, deny)
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewLocalBundle).ToSelf()
	m.Provide(NewAsyncLifecycle).ToSelf()
	m.Provide(NewRegistry).ToSelf()
	return m
}
