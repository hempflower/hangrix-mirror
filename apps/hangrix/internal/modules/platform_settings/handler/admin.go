// Package handler exposes the platform_settings admin HTTP surface.
// Mounted at /api/admin/platform-settings; cookie + RequireAdmin gated.
package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/domain"
)

type AdminHandler struct {
	store      domain.Store
	registry   *domain.Registry
	middleware authdomain.Middleware
}

type AdminHandlerDeps struct {
	Store      domain.Store
	Registry   *domain.Registry
	Middleware authdomain.Middleware
}

func NewAdminHandler(deps *AdminHandlerDeps) *AdminHandler {
	return &AdminHandler{
		store:      deps.Store,
		registry:   deps.Registry,
		middleware: deps.Middleware,
	}
}

func (h *AdminHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/admin/platform-settings", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Use(h.middleware.RequireAdmin)
		r.Get("/", h.list)
		r.Get("/{key}", h.get)
		r.Patch("/", h.patch)
		r.Patch("/{key}", h.patchKey)
	})
}

type settingDTO struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Description string `json:"description"`
	UpdatedAt   string `json:"updated_at"`
}

func toDTO(s domain.Setting) settingDTO {
	return settingDTO{
		Key:         s.Key,
		Value:       s.Value,
		Description: s.Description,
		UpdatedAt:   s.UpdatedAt.Format(time.RFC3339),
	}
}

// list returns every known setting. Always hits the DB.
func (h *AdminHandler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.List(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]settingDTO, 0, len(rows))
	for _, s := range rows {
		items = append(items, toDTO(s))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// get returns a single setting by key. Falls back to registry default
// when the key has no DB row.
func (h *AdminHandler) get(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		httpx.WriteError(w, http.StatusBadRequest, "key required")
		return
	}
	v, found, err := h.store.Get(r.Context(), key)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found || v == "" {
		if def, ok := h.registry.Lookup(key); ok {
			v = def.Default
			found = true
		}
	}
	if !found {
		httpx.WriteError(w, http.StatusNotFound, "setting not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, settingDTO{Key: key, Value: v})
}

// patchKey upserts a single setting. Body: {"value": "..."}
func (h *AdminHandler) patchKey(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		httpx.WriteError(w, http.StatusBadRequest, "key required")
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Value = strings.TrimSpace(body.Value)
	if body.Value == "" {
		httpx.WriteError(w, http.StatusBadRequest, "value required")
		return
	}
	// Validate the value can be parsed as a duration when the key is
	// a registered lifecycle key.
	if _, ok := h.registry.Lookup(key); ok {
		if _, err := time.ParseDuration(body.Value); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "value must be a valid Go duration (e.g. 1h, 168h)")
			return
		}
	}
	desc := ""
	if def, ok := h.registry.Lookup(key); ok {
		desc = def.Description
	}
	if err := h.store.Set(r.Context(), key, body.Value, desc); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Re-read so the response carries updated_at from the DB.
	v, found, err := h.store.Get(r.Context(), key)
	if err != nil || !found {
		httpx.WriteJSON(w, http.StatusOK, settingDTO{Key: key, Value: body.Value})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, settingDTO{Key: key, Value: v})
}

// patch upserts multiple settings at once. Body: {"items": [{"key":"...","value":"..."}]}
func (h *AdminHandler) patch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(body.Items) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "items required")
		return
	}
	for _, item := range body.Items {
		item.Key = strings.TrimSpace(item.Key)
		item.Value = strings.TrimSpace(item.Value)
		if item.Key == "" || item.Value == "" {
			httpx.WriteError(w, http.StatusBadRequest, "each item must have key and value")
			return
		}
		if _, ok := h.registry.Lookup(item.Key); ok {
			if _, err := time.ParseDuration(item.Value); err != nil {
				httpx.WriteError(w, http.StatusBadRequest, "value for "+item.Key+" must be a valid Go duration (e.g. 1h, 168h)")
				return
			}
		}
	}
	for _, item := range body.Items {
		desc := ""
		if def, ok := h.registry.Lookup(item.Key); ok {
			desc = def.Description
		}
		if err := h.store.Set(r.Context(), item.Key, item.Value, desc); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// RouteProvider compliance checked in module.go
