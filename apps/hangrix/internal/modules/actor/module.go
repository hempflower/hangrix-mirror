// Package actor wires the actor identity module. It sits beneath
// user, repo, runner, and workflow in the dependency graph — those
// modules expose the tables actor FKs reference. All modules above
// actor (issue, contribution, etc.) consume it for actor resolution.
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

	// Persistence: *infra.PostgresStore implements domain.Store.
	store := m.Provide(infra.NewPostgresStore)
	store.ToInterface(new(domain.Store))
	store.ToSelf()

	// Service: Resolver wraps Store. Bind as both concrete and interface
	// so callers can depend on domain.Resolver.
	resolver := m.Provide(service.NewResolver)
	resolver.ToInterface(new(domain.Resolver))
	resolver.ToSelf()

	// HTTP: GET /api/v1/actors/:id.
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))

	return m
}
