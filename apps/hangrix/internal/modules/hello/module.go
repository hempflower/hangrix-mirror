// Package hello is the "hello" feature module. It aggregates the module's
// internal layers (handler, and later domain / repo / infra) into a single
// *ioc.Module that main.go loads into the container.
package hello

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/hello/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	// future layers: m.Provide(infra.NewXyz).ToInterface(new(domain.Xyz))
	return m
}
