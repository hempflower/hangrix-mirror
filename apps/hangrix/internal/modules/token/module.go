// Package token wires the PAT module (Store + Validator + HTTP handler).
// Other modules depend on domain.Store or domain.Validator via the ioc
// container; they must never import this module's handler or infra packages
// directly.
package token

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	// The PostgresStore impl satisfies BOTH domain.Store and domain.Validator.
	// ioc disallows registering the same constructor twice (it indexes
	// providers by return type), but a single ProviderBinder can call
	// ToInterface multiple times — each call appends to the binding map.
	// This way callers asking for either interface get the same singleton.
	storeBinder := m.Provide(infra.NewPostgresStore)
	storeBinder.ToInterface(new(domain.Store))
	storeBinder.ToInterface(new(domain.Validator))

	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
