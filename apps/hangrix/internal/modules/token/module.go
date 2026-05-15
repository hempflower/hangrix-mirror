// Package token wires the PAT module's three layers:
//
//   - infra.PostgresRepo — Postgres-only persistence (Insert / List /
//     Revoke / GetByPrefix / TouchLastUsed). Satisfies domain.Repo.
//   - service.Service — composes Repo with bcrypt + regex + minting.
//     Satisfies domain.Store and domain.Validator on one type so a
//     single binding fans out to both interfaces.
//   - handler.Handler — HTTP. Depends only on domain.Store.
//
// Cross-module consumers (smart-HTTP, etc.) hold domain.Validator;
// they get the same Service instance via the ioc container.
package token

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()

	// Persistence: infra.PostgresRepo satisfies domain.Repo.
	m.Provide(infra.NewPostgresRepo).ToInterface(new(domain.Repo))

	// Service: one *service.Service binds to both Store and Validator
	// — same instance, same user-repo dep, no duplication.
	svc := m.Provide(service.New)
	svc.ToInterface(new(domain.Store))
	svc.ToInterface(new(domain.Validator))

	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
