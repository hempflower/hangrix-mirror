// Package automation wires the automation module: domain, service (Validator,
// Scheduler, Executor), infra (Store, RepoLister), and handler. The Scheduler
// is registered as a server.BackgroundJob; the Handler is registered as a
// server.RouteProvider.
package automation

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// Module returns the ioc module for the automation feature.
func Module() *ioc.Module {
	m := ioc.NewModule()

	// Infra: Postgres implementations. Each constructor mints its own
	// sqlc Queries from the pool — Queries is a stateless wrapper, so
	// there's no benefit to a shared instance.
	m.Provide(infra.NewPostgresStore).ToInterface(new(domain.Store))
	m.Provide(infra.NewRepoLister).ToInterface(new(domain.RepoLister))

	// Service: business logic.
	m.Provide(service.NewValidator).ToSelf()
	m.Provide(service.NewExecutor).ToSelf()
	m.Provide(service.NewScheduler).ToInterface(new(server.BackgroundJob))

	// Handler: HTTP routes.
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))

	return m
}
