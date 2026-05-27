// Package handler exposes the actor module's HTTP surface. Currently
// minimal — the Resolver is consumed by other modules via ioc, not via
// REST. Future endpoints (admin actor list, audit lookup) will be
// added here.
package handler

import (
	"github.com/go-chi/chi/v5"
)

// Handler satisfies server.RouteProvider.
type Handler struct{}

// NewHandler is the ioc constructor.
func NewHandler() *Handler {
	return &Handler{}
}

// RegisterRoutes is a no-op — no public HTTP surface yet.
func (h *Handler) RegisterRoutes(r chi.Router) {}
