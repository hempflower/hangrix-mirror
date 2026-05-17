package tools

import (
	"fmt"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/platform"
	"github.com/hangrix/hangrix/pkg/ioc"
)

type Deps struct {
	Cfg *config.Config
}

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
func NewRegistry(deps *Deps) *Registry {
	allow, err := ParseToolCatalog(deps.Cfg.ToolCatalog)
	if err != nil {
		panic(fmt.Errorf("tools: parse catalog: %w", err))
	}
	var platformTools []local.Tool
	if base := deps.Cfg.PlatformToolsBaseURL(); base != "" {
		client := platform.NewClient(base, deps.Cfg.SessionToken)
		platformTools = platform.All(client)
	}
	return Build(local.All(), platformTools, allow)
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewRegistry).ToSelf()
	return m
}
