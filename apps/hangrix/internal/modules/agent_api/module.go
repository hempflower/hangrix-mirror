// Package agent_api wires the agent-facing HTTP API: a plain REST handler
// in handler/ (POST /api/agent/tools/{name}, bearer-authed with the
// session token), tool implementations in service/, and the thin Tool
// descriptor in domain/. Despite the historical "MCP" name this is not an
// MCP / JSON-RPC server — it is an ordinary JSON-over-HTTP API.
//
// Cross-module dependencies all flow through domain interfaces (issue,
// repo, runner, git, agent_session). The module imports none of the
// other modules' handler or infra packages — same pattern the rest of
// the codebase uses to keep boundaries clean.
package agent_api

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_api/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_api/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(service.NewRegistry).ToSelf()
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
