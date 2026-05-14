// Package repo wires the repo module: domain.Store (Postgres), the Storage
// filesystem helper, and the HTTP handler. Other modules depend only on
// domain.Store via the ioc container; they must never import infra or
// handler directly.
package repo

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(infra.NewPostgresStore).ToInterface(new(domain.Store))
	m.Provide(infra.NewPostgresProtectionStore).ToInterface(new(domain.ProtectionStore))
	m.Provide(infra.NewStorage).ToSelf()
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
