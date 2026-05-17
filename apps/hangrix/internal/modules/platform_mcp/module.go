// Package platform_mcp wires the platform MCP server: HTTP + JSON-RPC
// handler in handler/, tool implementations in service/, thin Tool
// descriptor in domain/.
//
// Cross-module dependencies all flow through domain interfaces (issue,
// repo, runner, git, agent_session). The module imports none of the
// other modules' handler or infra packages — same pattern the rest of
// the codebase uses to keep boundaries clean.
package platform_mcp

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_mcp/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_mcp/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(service.NewRegistry).ToSelf()
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
