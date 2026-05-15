package tools

import (
	"context"
	"fmt"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/mcp"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
	"github.com/hangrix/hangrix/pkg/ioc"
)

type Deps struct {
	Cfg *config.Config
	MCP *mcp.Client // typed-nil when no platform MCP endpoint is configured
}

// NewRegistry parses HANGRIX_TOOL_CATALOG and builds the merged
// (local + remote) registry. Both steps happen exactly once at init
// time; once built the registry is read-only and safe to share across
// the runtime loop's concurrent tool calls.
//
// Both Build and ParseToolCatalog can fail (a malformed catalog
// allowlist, an unreachable MCP server). ioc constructors can't
// return errors, so we panic and let main.go's recover translate.
func NewRegistry(deps *Deps) *Registry {
	allow, err := ParseToolCatalog(deps.Cfg.ToolCatalog)
	if err != nil {
		panic(fmt.Errorf("tools: parse catalog: %w", err))
	}
	// context.Background is fine here — Build only uses it for the
	// initial tools/list MCP call. The runtime loop's request-scoped
	// context is threaded through individual tool invocations later,
	// not through registry assembly.
	registry, err := Build(context.Background(), local.All(), deps.MCP, allow)
	if err != nil {
		panic(fmt.Errorf("tools: build: %w", err))
	}
	return registry
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewRegistry).ToSelf()
	return m
}
