// Package handler exposes the llm_provider admin HTTP surface mounted at
// /api/admin/llm/. Every route is RequireAdmin-gated; provider credentials
// are platform-level operations.
//
// Response shape rules:
//   - The provider's encrypted api_key is NEVER returned. A derived boolean
//     `has_api_key` lets the UI distinguish "configured" from "unset".
//   - Session-token issuance is no longer part of this module — every
//     agent_session in the runner module mints its own identity token
//     on creation. See modules/runner/handler.AdminHandler for the
//     equivalent admin surface (POST /api/admin/runners/{id}/sessions).
package handler

import (
	"encoding/json"
	"errors"
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/infra"
)

// providerNameRe matches the URL-path slug for a provider. Lower-case so
// the resulting `/v1/<name>/...` proxy path is unambiguous on case-insensitive
// filesystems and HTTP middleware.
var providerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// defaultUsageLimit is the page size when the caller passes no `limit`;
// maxUsageLimit caps a hostile caller asking for a million rows.
const (
	defaultUsageLimit = 100
	maxUsageLimit     = 500
)

type Handler struct {
	repo       domain.Repo
	usage      *infra.PostgresRepo
	middleware authdomain.Middleware
}

// HandlerDeps wires the same Postgres instance into both the narrow domain
// interface (for everything mutating) and the concrete impl (for the
// admin-only usage read, which has no need to sit on the cross-module
// interface).
type HandlerDeps struct {
	Repo       domain.Repo
	Usage      *infra.PostgresRepo
	Middleware authdomain.Middleware
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{repo: deps.Repo, usage: deps.Usage, middleware: deps.Middleware}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/admin/llm", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Use(h.middleware.RequireAdmin)

		r.Post("/providers", h.createProvider)
		r.Get("/providers", h.listProviders)
		r.Get("/providers/{name}", h.getProvider)
		r.Patch("/providers/{name}", h.patchProvider)
		r.Post("/providers/{name}/disabled", h.setProviderDisabled)
		r.Delete("/providers/{name}", h.deleteProvider)

		r.Get("/usage", h.listUsage)
		r.Get("/usage/{id}", h.getUsage)
	})
}

// ---- provider DTOs ----

// publicProvider intentionally omits the encrypted api key. `has_api_key`
// surfaces "is something configured" so the UI can show a green dot without
// ever transporting the secret (sealed or otherwise).
type publicProvider struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	BaseURL       string    `json:"base_url"`
	HasAPIKey     bool      `json:"has_api_key"`
	AllowedModels []string  `json:"allowed_models"`
	Disabled      bool      `json:"disabled"`
	CreatedBy     int64     `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func toPublicProvider(p *domain.Provider) publicProvider {
	return publicProvider{
		ID:            p.ID,
		Name:          p.Name,
		Type:          string(p.Type),
		BaseURL:       p.BaseURL,
		HasAPIKey:     p.ApiKey != "",
		AllowedModels: sliceOrEmpty(p.AllowedModels),
		Disabled:      p.Disabled,
		CreatedBy:     p.CreatedBy,
		CreatedAt:     p.CreatedAt,
		UpdatedAt:     p.UpdatedAt,
	}
}

// sliceOrEmpty maps a nil slice to an empty slice so JSON encoders emit `[]`
// instead of `null`. Keeps the frontend's typed lists trivially safe.
func sliceOrEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- provider routes ----

type createProviderReq struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	BaseURL       string   `json:"base_url"`
	APIKey        string   `json:"api_key"`
	AllowedModels []string `json:"allowed_models"`
}

func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)

	var req createProviderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Type = strings.TrimSpace(req.Type)
	if !providerNameRe.MatchString(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	if !domain.ProviderType(req.Type).Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "invalid type")
		return
	}
	if req.APIKey == "" {
		if domain.ProviderType(req.Type) == domain.ProviderTypeMock {
			req.APIKey = "mock" // placeholder — mock provider never uses the key
		} else {
			httpx.WriteError(w, http.StatusBadRequest, "api_key is required")
			return
		}
	}

	in := &domain.Provider{
		Name:          req.Name,
		Type:          domain.ProviderType(req.Type),
		BaseURL:       strings.TrimSpace(req.BaseURL),
		ApiKey:        req.APIKey,
		AllowedModels: sliceOrEmpty(req.AllowedModels),
		CreatedBy:     caller.ID,
	}
	out, err := h.repo.CreateProvider(r.Context(), in)
	if err != nil {
		if errors.Is(err, domain.ErrProviderConflict) {
			httpx.WriteError(w, http.StatusConflict, "name already taken")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toPublicProvider(out))
}

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := h.repo.ListProviders(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicProvider, 0, len(rows))
	for _, p := range rows {
		items = append(items, toPublicProvider(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getProvider(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadProviderByName(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublicProvider(p))
}

type patchProviderReq struct {
	BaseURL       *string  `json:"base_url,omitempty"`
	APIKey        *string  `json:"api_key,omitempty"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	Disabled      *bool    `json:"disabled,omitempty"`
}

// patchProvider applies a partial update. Name and type are intentionally
// immutable — changing either would invalidate every session token bound to
// the row, so the contract is "delete and recreate" instead.
func (h *Handler) patchProvider(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.loadProviderByName(w, r)
	if !ok {
		return
	}
	var req patchProviderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	updated := *existing
	updated.ApiKey = "" // empty signals "leave the sealed blob alone"

	if req.BaseURL != nil {
		updated.BaseURL = strings.TrimSpace(*req.BaseURL)
	}
	if req.APIKey != nil && *req.APIKey != "" {
		updated.ApiKey = *req.APIKey
	}
	if req.AllowedModels != nil {
		updated.AllowedModels = sliceOrEmpty(req.AllowedModels)
	}
	if req.Disabled != nil {
		updated.Disabled = *req.Disabled
	}

	out, err := h.repo.UpdateProvider(r.Context(), &updated)
	if err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "provider not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublicProvider(out))
}

type setDisabledReq struct {
	Disabled bool `json:"disabled"`
}

// setProviderDisabled is the one-shot enable/disable toggle. Separate from
// patchProvider so the admin UI can flip a switch without round-tripping
// base_url / allowed_models (which would race a concurrent edit).
func (h *Handler) setProviderDisabled(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.loadProviderByName(w, r)
	if !ok {
		return
	}
	var req setDisabledReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	out, err := h.repo.SetProviderDisabled(r.Context(), existing.ID, req.Disabled)
	if err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "provider not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublicProvider(out))
}

func (h *Handler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadProviderByName(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteProvider(r.Context(), p.ID); err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "provider not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- usage ----

type publicUsage struct {
	ID               int64     `json:"id"`
	SessionID        *int64    `json:"session_id,omitempty"`
	ProviderID       int64     `json:"provider_id"`
	ProviderName     string    `json:"provider_name"`
	Model            string    `json:"model"`
	PromptTokens     int32     `json:"prompt_tokens"`
	CompletionTokens int32     `json:"completion_tokens"`
	TotalTokens      int32     `json:"total_tokens"`
	LatencyMS        int32     `json:"latency_ms"`
	StatusCode       int32     `json:"status_code"`
	ErrorMessage     string    `json:"error_message,omitempty"`
	RequestPath      string    `json:"request_path,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

func (h *Handler) listUsage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := defaultUsageLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		if n > maxUsageLimit {
			n = maxUsageLimit
		}
		limit = n
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid offset")
			return
		}
		offset = n
	}

	var providerID *int64
	if name := strings.TrimSpace(q.Get("provider")); name != "" {
		if !providerNameRe.MatchString(name) {
			httpx.WriteError(w, http.StatusBadRequest, "invalid provider")
			return
		}
		p, err := h.repo.GetProviderByName(r.Context(), name)
		if err != nil {
			if errors.Is(err, domain.ErrProviderNotFound) {
				httpx.WriteError(w, http.StatusNotFound, "provider not found")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		providerID = &p.ID
	}

	var since *time.Time
	if raw := strings.TrimSpace(q.Get("since")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid since (RFC3339 required)")
			return
		}
		since = &t
	}

	rows, err := h.usage.ListUsage(r.Context(), providerID, since, offset, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := h.usage.CountUsage(r.Context(), providerID, since)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicUsage, 0, len(rows))
	for _, u := range rows {
		items = append(items, publicUsage{
			ID:               u.ID,
			SessionID:        u.SessionID,
			ProviderID:       u.ProviderID,
			ProviderName:     u.ProviderName,
			Model:            u.Model,
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			TotalTokens:      u.TotalTokens,
			LatencyMS:        u.LatencyMS,
			StatusCode:       u.StatusCode,
			ErrorMessage:     u.ErrorMessage,
			RequestPath:      u.RequestPath,
			CreatedAt:        u.CreatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// publicUsageDetail is the single-row DTO for GET /api/admin/llm/usage/{id}.
// It includes the large body columns the list endpoint deliberately omits.
type publicUsageDetail struct {
	ID           int64     `json:"id"`
	ProviderName string    `json:"provider_name"`
	Model        string    `json:"model"`
	CreatedAt    time.Time `json:"created_at"`
	StatusCode   int32     `json:"status_code"`
	RequestBody  string    `json:"request_body"`
	ResponseBody string    `json:"response_body"`
}

func (h *Handler) getUsage(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}

	row, err := h.usage.GetUsageByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "usage record not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httpx.WriteJSON(w, http.StatusOK, publicUsageDetail{
		ID:           row.ID,
		ProviderName: row.ProviderName,
		Model:        row.Model,
		CreatedAt:    row.CreatedAt,
		StatusCode:   row.StatusCode,
		RequestBody:  row.RequestBody,
		ResponseBody: row.ResponseBody,
	})
}

// ---- helpers ----

func (h *Handler) loadProviderByName(w http.ResponseWriter, r *http.Request) (*domain.Provider, bool) {
	name := chi.URLParam(r, "name")
	if !providerNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return nil, false
	}
	p, err := h.repo.GetProviderByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "provider not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return p, true
}
