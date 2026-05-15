package mcp

import (
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/config"
	"github.com/hangrix/hangrix/pkg/ioc"
)

type Deps struct {
	Cfg *config.Config
}

// NewProvider returns a configured client, or a typed-nil *Client when
// HANGRIX_PLATFORM_MCP_ENDPOINT is unset. Returning typed-nil (rather
// than skipping registration) keeps the container's dependency graph
// stable across "with platform" and "smoke test" deployments — the
// toolregistry consumer nil-checks before issuing remote calls.
func NewProvider(deps *Deps) *Client {
	if deps.Cfg.MCPEndpoint == "" {
		return nil
	}
	return New(deps.Cfg.MCPEndpoint, deps.Cfg.SessionToken)
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewProvider).ToSelf()
	return m
}
