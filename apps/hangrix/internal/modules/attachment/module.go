// Package attachment wires the platform-level attachment feature:
// persistence (Postgres), the HTTP handler, the business-logic service,
// and cross-module interfaces consumed by agent_api and issue.
package attachment

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()

	// Persistence: PostgresStore satisfies Store and CommentAttachmentStore.
	storeBinder := m.Provide(infra.NewPostgresStore)
	storeBinder.ToInterface(new(domain.Store))
	storeBinder.ToInterface(new(domain.CommentAttachmentStore))

	// Business logic: validation, hashing, on-disk writes.
	svcBinder := m.Provide(service.NewService)
	svcBinder.ToSelf()
	svcBinder.ToInterface(new(domain.Uploader))

	// HTTP handler doubles as a RouteProvider.
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))

	return m
}
