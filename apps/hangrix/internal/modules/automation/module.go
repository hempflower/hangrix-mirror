// Package automation wires the automation module: domain, service (Validator,
// Scheduler, Executor), infra (Store, RepoLister), and handler. The Scheduler
// is registered as a server.BackgroundJob; the Handler is registered as a
// server.RouteProvider.
package automation

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/infra/automationdb"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// Module returns the ioc module for the automation feature.
func Module() *ioc.Module {
	m := ioc.NewModule()

	// Infra: Postgres implementations. Provide automationdb.Queries from
	// the pool so both PostgresStore and RepoLister can use it.
	m.Provide(func(pool *pgxpool.Pool) *automationdb.Queries {
		return automationdb.New(pool)
	}).ToSelf()
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
