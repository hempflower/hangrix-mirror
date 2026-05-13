// Package healthz is the liveness/readiness feature module.
package healthz

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/healthz/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
