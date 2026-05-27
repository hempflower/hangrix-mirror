// Package plan_engine wires the plan progression engine: persistence
// (plan_state table), the deterministic engine service, and the
// background ticker. Other modules consume domain.Engine via the ioc
// container; nothing imports this package directly.
package plan_engine

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/plan_engine/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/plan_engine/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/plan_engine/service"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()

	// Persistence.
	m.Provide(infra.NewPlanStateStore).ToInterface(new(domain.PlanStateStore))

	// Engine service — deterministic component, not an agent.
	engineBinder := m.Provide(service.NewEngine)
	engineBinder.ToInterface(new(domain.Engine))
	engineBinder.ToSelf() // allow handler to depend on *service.Engine for ticker start

	return m
}
