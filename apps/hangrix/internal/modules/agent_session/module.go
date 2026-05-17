// Package agent_session wires the M7a Phase 2 per-role session
// orchestrator: takes issue lifecycle events (issue.opened / closed /
// merged in M7a; M7b adds more), reads `.hangrix/agents.yml` at the host
// repo's base-branch tip, and produces one agent_sessions row per
// matching role. Persistence is owned by the runner module; this module
// only adds the higher-level semantics (snapshot fields, idempotent
// spawn, archive-on-close, audit query view).
package agent_session

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()

	// Concrete implementations bound to their narrow domain interface
	// each — the issue module depends only on domain.Spawner +
	// domain.Archiver; the admin handler depends only on
	// domain.Auditor. None of them sees the wider service struct.
	m.Provide(service.NewGitBlobReader).ToInterface(new(domain.HostBlobReader))
	m.Provide(service.NewSpawner).ToInterface(new(domain.Spawner))
	m.Provide(service.NewArchiver).ToInterface(new(domain.Archiver))
	m.Provide(service.NewAuditor).ToInterface(new(domain.Auditor))

	m.Provide(handler.NewAdminHandler).ToInterface(new(server.RouteProvider))
	return m
}
