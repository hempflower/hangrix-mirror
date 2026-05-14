// Package issue wires the M4 issue feature: persistence (Postgres), the
// branch-write guard that gates pushes onto issue branches, the push
// observer that records commit_pushed events, and the HTTP handler. Other
// modules consume domain.Store via the ioc container; nothing imports this
// package directly.
package issue

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/infra"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(infra.NewPostgresStore).ToInterface(new(domain.Store))
	m.Provide(infra.NewIssueGuard).ToInterface(new(repodomain.BranchWriteGuard))

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
