// Package handler exposes the platform's agent API over a GitHub-style v1
// REST surface mounted at /api/v1. Bearer-auth uses the `hgxs_` session
// token; the per-request Actor is derived from the session and carried
// through the v1 handlers (see auth.go / v1_routes.go).
//
// Resources are addressed by path (issues, comments, contributions,
// reviews, todos, releases, attachments, …) and operated on with the
// usual HTTP verbs. Errors come back as 4xx/5xx with `{ "error": "…" }`;
// the v1 handlers own their own response shapes (see respond.go).
package handler

import (
	"github.com/go-chi/chi/v5"

	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

type Handler struct {
	api       AgentAPI
	validator runnerdomain.SessionTokenValidator
}

type HandlerDeps struct {
	API       AgentAPI
	Validator runnerdomain.SessionTokenValidator
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		api:       deps.API,
		validator: deps.Validator,
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	// v1 REST API — resource-oriented, GitHub-style.
	if h.api != nil {
		r.Route("/api/v1", func(r chi.Router) {
			r.Use(BearerAuth(h.validator))
			RegisterV1Routes(r, h.api)
		})
	}
}
