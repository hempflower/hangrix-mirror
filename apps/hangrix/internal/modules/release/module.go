// Package release wires the release module: domain.Store (Postgres),
// domain.AssetStore (Postgres), the asset filesystem helper, and the
// HTTP handler. Other modules depend only on domain.Store /
// domain.AssetStore via the ioc container.
package release

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/release/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/release/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/release/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(infra.NewPostgresStore).ToInterface(new(domain.Store))
	m.Provide(infra.NewPostgresAssetStore).ToInterface(new(domain.AssetStore))
	m.Provide(infra.NewAssetStorage).ToSelf()
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
