// Package runner wires the runner registry + agent-session machinery.
//
// Persistence vs. service split:
//
//   - infra.PostgresRepo owns Postgres mutations (create, claim, append,
//     etc.) and the stateful enrollment redemption. It satisfies
//     domain.Repo and domain.EnrollValidator.
//
//   - service.AgentTokenValidator / service.SessionTokenValidator are
//     stateless validators — they compose Repo lookups with bcrypt and
//     belong in the service layer rather than persistence.
//
// Two RouteProviders are mounted:
//   - handler.AdminHandler   at /api/admin/runners/*  (cookie + RequireAdmin)
//   - handler.AgentHandler   at /api/runner/*         (Bearer hgxr_/hgxe_)
//
// The admin handler holds the cryptobox so it can seal the session-token
// plaintext at session-creation time; the agent handler holds it to
// decrypt the same blob when a runner claims the task. The plaintext is
// never written back into a response the admin user sees — the runner
// receives it over its own authenticated Bearer channel.
package runner

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()

	// Persistence: *infra.PostgresRepo only implements domain.Repo
	// (narrow CRUD + the transactional primitives the service layer
	// orchestrates against). Token validators / enroller compose Repo
	// with crypto in service/, never the other way around.
	repo := m.Provide(infra.NewPostgresRepo)
	repo.ToInterface(new(domain.Repo))
	repo.ToSelf()

	// Service: stateless validators (read paths) + Enroller (write
	// path via callback into Repo's transaction). Each composes Repo
	// lookups with bcrypt; none of them touch pgx.
	m.Provide(service.NewAgentTokenValidator).
		ToInterface(new(domain.AgentValidator))
	m.Provide(service.NewSessionTokenValidator).
		ToInterface(new(domain.SessionTokenValidator))
	m.Provide(service.NewEnroller).
		ToInterface(new(domain.EnrollValidator))

	m.Provide(handler.NewAdminHandler).ToInterface(new(server.RouteProvider))
	m.Provide(handler.NewAgentHandler).ToInterface(new(server.RouteProvider))
	return m
}
