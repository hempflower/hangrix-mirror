// Package platform_api wires the agent-facing HTTP API: a legacy RPC-style
// POST /api/agent/tools/{name} (deprecated, kept for compatibility during
// v1 migration) and the new GitHub-style REST surface under /api/v1/.
//
// Cross-module dependencies all flow through domain interfaces (issue,
// repo, runner, git, agent_session). The module imports none of the
// other modules' handler or infra packages — same pattern the rest of
// the codebase uses to keep boundaries clean.
package platform_api

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(service.NewRegistry).ToSelf()
	m.Provide(service.NewAPIService).ToInterface(new(handler.AgentAPI))
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
