// Package handler exposes the platform_settings module's admin HTTP surface.
// Mounted at /api/admin/platform-settings; cookie + RequireAdmin gated.
package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/domain"
)

type AdminHandler struct {
	store      domain.Store
	middleware authdomain.Middleware
}

type AdminHandlerDeps struct {
	Store      domain.Store
	Middleware authdomain.Middleware
}

func NewAdminHandler(deps *AdminHandlerDeps) *AdminHandler {
	return &AdminHandler{
		store:      deps.Store,
		middleware: deps.Middleware,
	}
}

func (h *AdminHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/admin/platform-settings", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Use(h.middleware.RequireAdmin)
		r.Get("/", h.listSettings)
		r.Patch("/{key}", h.patchSetting)
	})
}

type settingDTO struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at,omitempty"`
	UpdatedBy *int64 `json:"updated_by,omitempty"`
}

func (h *AdminHandler) listSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.List(r.Context(), "")
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]settingDTO, 0, len(rows))
	for _, s := range rows {
		dto := settingDTO{Key: string(s.Key), Value: s.Value}
		if !s.UpdatedAt.IsZero() {
			dto.UpdatedAt = s.UpdatedAt.Format("2006-01-02T15:04:05Z")
			dto.UpdatedBy = s.UpdatedBy
		}
		items = append(items, dto)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type patchReq struct {
	Value string `json:"value"`
}

func (h *AdminHandler) patchSetting(w http.ResponseWriter, r *http.Request) {
	key := domain.Key(strings.TrimSpace(chi.URLParam(r, "key")))
	if key == "" {
		httpx.WriteError(w, http.StatusBadRequest, "key required")
		return
	}

	var req patchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Value = strings.TrimSpace(req.Value)
	if req.Value == "" {
		httpx.WriteError(w, http.StatusBadRequest, "value required")
		return
	}

	// Get the authenticated user's id for updated_by.
	user, ok := authdomain.UserFromRequest(r)
	var updatedBy int64
	if ok && user != nil {
		updatedBy = user.ID
	}

	if err := h.store.Set(r.Context(), key, req.Value, updatedBy); err != nil {
		switch {
		case err == domain.ErrUnknownKey:
			httpx.WriteError(w, http.StatusNotFound, "unknown setting key")
		case err == domain.ErrInvalidValue:
			httpx.WriteError(w, http.StatusUnprocessableEntity, "invalid value for setting")
		default:
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
