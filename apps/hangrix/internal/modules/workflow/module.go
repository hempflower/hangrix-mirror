// Package workflow wires the workflow module: domain, service, infra, and
// handler. The Service is registered as domain.Dispatcher for cross-module
// runner integration. The Handler is registered as server.RouteProvider.
package workflow

import (
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// Module returns the ioc module for the workflow feature.
func Module() *ioc.Module {
	m := ioc.NewModule()

	// Infra: Postgres implementation of domain.Store
	m.Provide(infra.NewPostgresRepo).ToInterface(new(domain.Store))

	// Service: business logic; single instance satisfies Dispatcher,
	// TagEventTrigger, and WorkflowTokenValidator for cross-module
	// integration.
	svc := m.Provide(service.New)
	svc.ToSelf()
	svc.ToInterface(new(domain.Dispatcher))
	svc.ToInterface(new(domain.TagEventTrigger))
	svc.ToInterface(new(domain.WorkflowTokenValidator))

	// PushObserver: triggers repo.push_tag workflows on git tag push.
	m.Provide(handler.NewPushObserver).ToInterface(new(repodomain.PushObserver))

	// Handler: HTTP routes
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))

	return m
}
