// Package llm_proxy wires the OpenAI-Response-API-compatible HTTP proxy
// that fronts platform-registered upstream providers. It is the runtime
// twin of modules/llm_provider: the provider module owns persistence and
// admin CRUD; this module owns request-time dispatch.
//
// Cross-module wiring:
//
//   - domain.Repo (write-capable) is consumed so the handler can call
//     TouchSessionTokenLastUsed after a successful upstream call. The
//     proxy never writes providers or tokens; it just needs that one
//     touch-update plus RecordUsage.
//   - domain.Validator is consumed by the Bearer-auth middleware.
//
// Both interfaces are satisfied by the same *infra.PostgresRepo instance
// the llm_provider module already binds into the container.
package llm_proxy

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_proxy/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
