// Package dashboard wires the admin dashboard aggregation module.
// It depends on the llm_provider domain for provider name → ID resolution
// and directly queries the database for cross-module aggregations.
package dashboard

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/dashboard/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/dashboard/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(infra.NewPostgresRepo).ToSelf()
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
