// Package auth wires the session store, middleware, and HTTP handlers for the
// authentication feature. Other modules depend on domain.Middleware (gates)
// and domain.SessionStore (rare); never on the auth handler or repo directly.
package auth

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(infra.NewRedisSessionStore).ToInterface(new(domain.SessionStore))
	m.Provide(NewMiddleware).ToInterface(new(domain.Middleware))
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
