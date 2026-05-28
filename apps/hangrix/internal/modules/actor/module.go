// Package actor wires the actor module: persistence (PostgresStore),
// the Resolver (runtime-to-actor bridge), and the minimal HTTP handler.
// Other modules consume domain.Store and domain.Resolver via the ioc
// container.
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

	// Persistence: *infra.PostgresStore satisfies domain.Store.
	store := m.Provide(infra.NewPostgresStore)
	store.ToInterface(new(domain.Store))
	store.ToSelf()

	// Service: Resolver (runtime-to-actor bridge). Singleton,
	// consumed by platform_api, issue handler, and spawner.
	m.Provide(service.NewResolver).ToInterface(new(domain.Resolver))

	// Handler: minimal RouteProvider (no public routes yet).
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))

	return m
}
