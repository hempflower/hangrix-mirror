// Package issue_gate wires the cross-cutting issue terminal-state check
// consumed by every agent-facing surface (platform API, LLM proxy, git
// push, session spawner). It depends on issue/domain.Store to look up
// the issue state; consumers depend on domain.IssueActivityGate.
package issue_gate

import (
	issuegatedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue_gate/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue_gate/service"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(service.NewGate).ToInterface(new(issuegatedomain.IssueActivityGate))
	return m
}
