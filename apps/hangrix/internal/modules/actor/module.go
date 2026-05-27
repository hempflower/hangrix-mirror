// Package actor wires the normalised actor identity table, persistence
// (domain.Store via infra.PostgresRepo), and the runtime resolution
// bridge (service.Resolver). The module depends on user, repo, runner,
// and workflow modules for FK-backed migrations — those modules must
// be loaded before this one in the ioc container.
//
// Two singletons are registered:
//   - domain.Store (backed by *infra.PostgresRepo) — for direct actor
//     table access.
//   - *service.Resolver — the runtime-to-actor bridge consumed by
//     downstream modules.
package actor

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()

	// Persistence: infra.PostgresRepo implements domain.Store.
	repo := m.Provide(infra.NewPostgresRepo)
	repo.ToInterface(new(domain.Store))
	repo.ToSelf()

	// Service: the singleton Resolver bridges pkg/actor.Ref ↔ actor IDs.
	m.Provide(service.NewResolver).ToSelf()

	// Handler: no-op RouteProvider in Phase 3b; satisfies the wiring contract.
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))

	return m
}
