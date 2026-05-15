// Package llm_provider wires the LLM-provider registry: the Postgres repo
// (which simultaneously satisfies Repo, Lookup, and Validator) plus the
// admin-only HTTP handler. Cross-module consumers — the M6b agent runtime
// and modules/llm_proxy — depend on the narrow interfaces in domain/ via
// the ioc container; they must never import infra or handler directly.
package llm_provider

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	// The Postgres impl satisfies Repo / Lookup / Validator; bind one
	// provider to all three so every caller (handlers here, the proxy
	// elsewhere) shares the same connection pool and migration state.
	binder := m.Provide(infra.NewPostgresRepo)
	binder.ToInterface(new(domain.Repo))
	binder.ToInterface(new(domain.Lookup))
	binder.ToInterface(new(domain.Validator))
	// Bind to *infra.PostgresRepo as well so the admin handler can reach
	// the usage-read path (not on the cross-module interface — only this
	// module's handler renders the usage table).
	binder.ToSelf()

	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
