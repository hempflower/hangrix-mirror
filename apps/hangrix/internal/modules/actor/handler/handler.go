// Package handler provides the actor module's HTTP surface. In Phase 3b
// the actor module is purely a service consumed by other modules — no
// public REST routes yet. The handler exists as a no-op RouteProvider so
// the module wires cleanly into the server.
package handler

import (
	"github.com/go-chi/chi/v5"
)

// Handler is the actor HTTP handler. Currently a no-op RouteProvider.
type Handler struct{}

// NewHandler creates a new Handler.
func NewHandler() *Handler {
	return &Handler{}
}

// RegisterRoutes satisfies server.RouteProvider. No routes are registered
// in Phase 3b — the actor module is consumed by other modules via the ioc
// container's service.Resolver.
func (h *Handler) RegisterRoutes(r chi.Router) {}
