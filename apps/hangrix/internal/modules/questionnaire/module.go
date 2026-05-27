// Package questionnaire wires the questionnaire feature module into the ioc
// container. It follows the modular-monolith pattern: handler → service →
// infra (PostgresStore).
package questionnaire

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/questionnaire/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/questionnaire/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/questionnaire/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/questionnaire/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()

	// Persistence: PostgresStore satisfies Store + AnswerStore.
	storeBinder := m.Provide(infra.NewPostgresStore)
	storeBinder.ToInterface(new(domain.Store))
	storeBinder.ToInterface(new(domain.AnswerStore))

	// Service: wraps Store + AnswerStore + optional EventPublisher.
	m.Provide(service.NewService).ToInterface(new(domain.Service))

	// Handler: user-facing routes.
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))

	return m
}
