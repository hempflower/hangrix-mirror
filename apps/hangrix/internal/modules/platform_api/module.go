// Package platform_api wires the agent-facing HTTP API: the GitHub-style
// REST surface under /api/v1/. The Actor's repo permission (read/write)
// is the coarse server-side access boundary; fine-grained per-tool
// capability is shaped agent-side via the role's tool blacklist.
//
// Cross-module dependencies all flow through domain interfaces (issue,
// repo, runner, git, agent_session, workflow). The module imports none of
// the other modules' handler or infra packages — same pattern the rest of
// the codebase uses to keep boundaries clean.
package platform_api

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/service"
	workflowdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(service.NewRegistry).ToSelf()
	m.Provide(service.NewAPIService).ToInterface(new(handler.AgentAPI))
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))

	// CIStatusObserver bridges workflow run status changes to ci.status_changed
	// trigger events via Spawner.OnTrigger.
	obs := m.Provide(service.NewCIStatusObserver)
	obs.ToSelf()
	obs.ToInterface(new(workflowdomain.RunStatusObserver))

	return m
}
