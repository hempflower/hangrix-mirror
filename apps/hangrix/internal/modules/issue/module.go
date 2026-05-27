// Package issue wires the issue feature: persistence (Postgres), the push
// observer that records commit_pushed events on `issue/<n>` branches, and
// the HTTP handler. Other modules consume domain.Store via the ioc
// container; nothing imports this package directly.
package issue

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/service"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()

	// Persistence: PostgresStore satisfies Store, AttachmentStore, and ContributionStore.
	storeBinder := m.Provide(infra.NewPostgresStore)
	storeBinder.ToInterface(new(domain.Store))
	storeBinder.ToInterface(new(domain.AttachmentStore))
	storeBinder.ToInterface(new(domain.ContributionStore))
	storeBinder.ToInterface(new(domain.TodoStore))
	storeBinder.ToInterface(new(domain.DependencyStore))

	// Attachment service: validation, hashing, on-disk writes.
	svcBinder := m.Provide(service.NewAttachmentService)
	svcBinder.ToSelf()
	svcBinder.ToInterface(new(domain.AttachmentUploader))

	// The handler doubles as a RouteProvider and is also a dependency of
	// the PushObserver — bind it through ToSelf (so the observer can
	// depend on *handler.Handler) AND ToInterface so the server picks it
	// up as a route source. See token/module.go for the same pattern.
	handlerBinder := m.Provide(handler.NewHandler)
	handlerBinder.ToSelf()
	handlerBinder.ToInterface(new(server.RouteProvider))

	m.Provide(handler.NewPushObserver).ToInterface(new(repodomain.PushObserver))
	return m
}
