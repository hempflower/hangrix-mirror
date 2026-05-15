// Package llm_proxy wires the OpenAI-Response-API-compatible HTTP proxy
// that fronts platform-registered upstream providers. It is the runtime
// twin of modules/llm_provider: the provider module owns persistence
// and admin CRUD; this module owns request-time dispatch.
//
// Cross-module wiring:
//
//   - llm_provider/domain.Lookup is consumed for: (a) model→provider
//     resolution at request time, and (b) usage-log writes after dispatch.
//   - runner/domain.SessionTokenValidator is consumed for Bearer-auth.
//     The session token represents agent identity — the proxy does not
//     care which agent_session a request comes from beyond logging the
//     id alongside the usage row.
//
// Both interfaces are satisfied by the respective modules' Postgres
// repos; the container binds them.
//
// upstream.Registry is built here from the default set of per-vendor
// adapters and shared with the handler. Adding a new vendor is one new
// upstream.Provider implementation + one entry in upstream.Default; the
// handler itself stays untouched.
package llm_proxy

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_proxy/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_proxy/upstream"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(upstream.Default).ToSelf()
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
