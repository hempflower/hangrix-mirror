// Package agents_config wires the parsers for the two M7a config files
// (`agent.yml` and `.hangrix/agents.yml`) plus the lock file. The
// module exposes only pure-function parsing today — no DB, no HTTP, no
// IPC — so Module() registers a single stateless *service.Parser and
// binds it ToSelf(). M7a Phase 2 will graft on the dispatcher and an
// admin handler that depend on this parser through ioc.
package agents_config

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agents_config/service"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// Module registers *service.Parser so downstream modules can take it
// on their *Deps. The Parser is stateless; ToSelf() is enough — no
// interface yet because M7a Phase 1 has no second implementation in
// sight.
func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(service.NewParser).ToSelf()
	return m
}
