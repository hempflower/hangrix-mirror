// Package org wires the org module: OrgRepo + Resolver (both backed by the
// same Postgres impl) plus the HTTP handler. Other modules depend on these
// interfaces via the ioc container; they must never import infra or handler
// directly.
package org

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	// The Postgres impl satisfies both interfaces; bind one provider to both
	// so callers consistently receive the same instance and avoid running
	// migrations twice.
	binder := m.Provide(infra.NewPostgresRepo)
	binder.ToInterface(new(domain.OrgRepo))
	binder.ToInterface(new(domain.Resolver))
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
