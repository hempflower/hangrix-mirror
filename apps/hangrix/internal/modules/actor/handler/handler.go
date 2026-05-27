// Package handler exposes the actor module's HTTP surface.
// GET /api/v1/actors/:id returns a single actor with its metadata.
package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
)

// Handler implements server.RouteProvider.
type Handler struct {
	store domain.Store
}

type HandlerDeps struct {
	Store domain.Store
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{store: deps.Store}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/actors/{id}", h.getByID)
}

// actorResponse is the public API projection of an Actor row.
type actorResponse struct {
	ID             int64  `json:"id"`
	Kind           string `json:"kind"`
	DisplayName    string `json:"display_name"`
	UserID         *int64 `json:"user_id,omitempty"`
	AgentSessionID *int64 `json:"agent_session_id,omitempty"`
	WorkflowRunID  *int64 `json:"workflow_run_id,omitempty"`
	RepoID         *int64 `json:"repo_id,omitempty"`
	RoleKey        string `json:"role_key,omitempty"`
	BotHandle      string `json:"bot_handle,omitempty"`
}

func (h *Handler) getByID(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	a, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrActorNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "actor not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "failed to get actor")
		return
	}

	resp := actorResponse{
		ID:             a.ID,
		Kind:           string(a.Kind),
		DisplayName:    a.DisplayName,
		UserID:         a.UserID,
		AgentSessionID: a.AgentSessionID,
		WorkflowRunID:  a.WorkflowRunID,
		RepoID:         a.RepoID,
		RoleKey:        a.RoleKey,
		BotHandle:      a.BotHandle,
	}

	httpx.WriteJSON(w, http.StatusOK, resp)
}
