// Package platform_settings wires the runtime-mutable, platform-wide
// key-value settings store. Registered before agent_session in main.go
// so the Reaper can resolve domain.Store from the ioc container.
package platform_settings

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()

	// Persistence: infra.PostgresRepo (internal — not exposed via domain).
	m.Provide(infra.NewPostgresRepo).ToSelf()

	// Service: caching Store, bound to domain.Store for consumers.
	m.Provide(service.NewStore).ToInterface(new(domain.Store))

	m.Provide(handler.NewAdminHandler).ToInterface(new(server.RouteProvider))
	return m
}
