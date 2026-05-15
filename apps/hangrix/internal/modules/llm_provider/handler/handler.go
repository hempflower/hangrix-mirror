// Package handler exposes the llm_provider admin HTTP surface mounted at
// /api/admin/llm/. Every route is RequireAdmin-gated; provider credentials
// and session-token issuance are platform-level operations.
//
// Response shape rules:
//   - The provider's encrypted api_key is NEVER returned. A derived boolean
//     `has_api_key` lets the UI distinguish "configured" from "unset".
//   - Session-token plaintext is returned exactly once, on the POST response.
//     List endpoints expose only prefix + metadata; hashed_key never leaves
//     the database.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

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
		r.Delete("/providers/{name}", h.deleteProvider)
		r.Post("/providers/{name}/default", h.setDefault)

		r.Post("/session-tokens", h.createSessionToken)
		r.Get("/session-tokens", h.listSessionTokens)
		r.Delete("/session-tokens/{id}", h.revokeSessionToken)

		r.Get("/usage", h.listUsage)
	})
}

// ---- provider DTOs ----

// publicProvider intentionally omits the encrypted api key. `has_api_key`
// surfaces "is something configured" so the UI can show a green dot without
// ever transporting the secret (sealed or otherwise).
type publicProvider struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	Type              string    `json:"type"`
	BaseURL           string    `json:"base_url"`
	HasAPIKey         bool      `json:"has_api_key"`
	AllowedModels     []string  `json:"allowed_models"`
	Visibility        string    `json:"visibility"`
	AllowedRepos      []string  `json:"allowed_repos"`
	RateLimitRPM      int32     `json:"rate_limit_rpm"`
	IsPlatformDefault bool      `json:"is_platform_default"`
	DefaultModel      string    `json:"default_model"`
	CreatedBy         int64     `json:"created_by"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func toPublicProvider(p *domain.Provider) publicProvider {
	return publicProvider{
		ID:                p.ID,
		Name:              p.Name,
		Type:              string(p.Type),
		BaseURL:           p.BaseURL,
		HasAPIKey:         p.ApiKey != "",
		AllowedModels:     sliceOrEmpty(p.AllowedModels),
		Visibility:        string(p.Visibility),
		AllowedRepos:      sliceOrEmpty(p.AllowedRepos),
		RateLimitRPM:      p.RateLimitRPM,
		IsPlatformDefault: p.IsPlatformDefault,
		DefaultModel:      p.DefaultModel,
		CreatedBy:         p.CreatedBy,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
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
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	BaseURL           string   `json:"base_url"`
	APIKey            string   `json:"api_key"`
	AllowedModels     []string `json:"allowed_models"`
	Visibility        string   `json:"visibility"`
	AllowedRepos      []string `json:"allowed_repos"`
	RateLimitRPM      int32    `json:"rate_limit_rpm"`
	DefaultModel      string   `json:"default_model"`
	IsPlatformDefault bool     `json:"is_platform_default"`
}

func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)

	var req createProviderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Type = strings.TrimSpace(req.Type)
	req.Visibility = strings.TrimSpace(req.Visibility)
	if req.Visibility == "" {
		req.Visibility = string(domain.VisibilityPlatform)
	}
	if !providerNameRe.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid name")
		return
	}
	if !domain.ProviderType(req.Type).Valid() {
		writeError(w, http.StatusBadRequest, "invalid type")
		return
	}
	if !domain.Visibility(req.Visibility).Valid() {
		writeError(w, http.StatusBadRequest, "invalid visibility")
		return
	}
	if req.APIKey == "" {
		writeError(w, http.StatusBadRequest, "api_key is required")
		return
	}

	in := &domain.Provider{
		Name:              req.Name,
		Type:              domain.ProviderType(req.Type),
		BaseURL:           strings.TrimSpace(req.BaseURL),
		ApiKey:            req.APIKey,
		AllowedModels:     sliceOrEmpty(req.AllowedModels),
		Visibility:        domain.Visibility(req.Visibility),
		AllowedRepos:      sliceOrEmpty(req.AllowedRepos),
		RateLimitRPM:      req.RateLimitRPM,
		IsPlatformDefault: req.IsPlatformDefault,
		DefaultModel:      strings.TrimSpace(req.DefaultModel),
		CreatedBy:         caller.ID,
	}
	out, err := h.repo.CreateProvider(r.Context(), in)
	if err != nil {
		if errors.Is(err, domain.ErrProviderConflict) {
			writeError(w, http.StatusConflict, "name already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// If the caller asked for this provider to be the platform default,
	// flip the bit atomically so the partial unique index isn't tripped.
	if in.IsPlatformDefault {
		if err := h.repo.SetPlatformDefault(r.Context(), out.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out, err = h.repo.GetProviderByID(r.Context(), out.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, toPublicProvider(out))
}

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := h.repo.ListProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicProvider, 0, len(rows))
	for _, p := range rows {
		items = append(items, toPublicProvider(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getProvider(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadProviderByName(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toPublicProvider(p))
}

type patchProviderReq struct {
	BaseURL           *string  `json:"base_url,omitempty"`
	APIKey            *string  `json:"api_key,omitempty"`
	AllowedModels     []string `json:"allowed_models,omitempty"`
	Visibility        *string  `json:"visibility,omitempty"`
	AllowedRepos      []string `json:"allowed_repos,omitempty"`
	RateLimitRPM      *int32   `json:"rate_limit_rpm,omitempty"`
	DefaultModel      *string  `json:"default_model,omitempty"`
	IsPlatformDefault *bool    `json:"is_platform_default,omitempty"`
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
		writeError(w, http.StatusBadRequest, "invalid body")
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
	if req.Visibility != nil {
		v := domain.Visibility(strings.TrimSpace(*req.Visibility))
		if !v.Valid() {
			writeError(w, http.StatusBadRequest, "invalid visibility")
			return
		}
		updated.Visibility = v
	}
	if req.AllowedRepos != nil {
		updated.AllowedRepos = sliceOrEmpty(req.AllowedRepos)
	}
	if req.RateLimitRPM != nil {
		updated.RateLimitRPM = *req.RateLimitRPM
	}
	if req.DefaultModel != nil {
		updated.DefaultModel = strings.TrimSpace(*req.DefaultModel)
	}
	if req.IsPlatformDefault != nil {
		updated.IsPlatformDefault = *req.IsPlatformDefault
	}

	out, err := h.repo.UpdateProvider(r.Context(), &updated)
	if err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "provider not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Honour the default toggle the same way create does. We re-route the
	// flip through SetPlatformDefault so the clear-other-rows side of the
	// invariant is enforced.
	if req.IsPlatformDefault != nil && *req.IsPlatformDefault {
		if err := h.repo.SetPlatformDefault(r.Context(), out.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out, err = h.repo.GetProviderByID(r.Context(), out.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, toPublicProvider(out))
}

func (h *Handler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadProviderByName(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteProvider(r.Context(), p.ID); err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "provider not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) setDefault(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadProviderByName(w, r)
	if !ok {
		return
	}
	if err := h.repo.SetPlatformDefault(r.Context(), p.ID); err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "provider not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, err := h.repo.GetProviderByID(r.Context(), p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toPublicProvider(out))
}

// ---- session-token DTOs / routes ----

// publicSessionToken is the response shape for list / DELETE; never carries
// the hashed key or the plaintext (the latter is on the create-response).
type publicSessionToken struct {
	ID           int64      `json:"id"`
	Prefix       string     `json:"prefix"`
	ProviderID   int64      `json:"provider_id"`
	ProviderName string     `json:"provider_name"`
	Model        string     `json:"model"`
	Label        string     `json:"label"`
	CreatedBy    int64      `json:"created_by"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

func toPublicSessionToken(t *domain.SessionToken, providerName string) publicSessionToken {
	return publicSessionToken{
		ID:           t.ID,
		Prefix:       t.Prefix,
		ProviderID:   t.ProviderID,
		ProviderName: providerName,
		Model:        t.Model,
		Label:        t.Label,
		CreatedBy:    t.CreatedBy,
		LastUsedAt:   t.LastUsedAt,
		ExpiresAt:    t.ExpiresAt,
		RevokedAt:    t.RevokedAt,
		CreatedAt:    t.CreatedAt,
	}
}

type createSessionTokenReq struct {
	ProviderName string  `json:"provider_name"`
	Model        string  `json:"model"`
	Label        string  `json:"label"`
	ExpiresAt    *string `json:"expires_at,omitempty"`
}

type createSessionTokenResp struct {
	Token publicSessionToken `json:"token"`
	// Plaintext is the wire-format hgxs_<prefix>_<secret>. Shown exactly
	// once; callers must store it client-side before navigating away.
	Plaintext string `json:"plaintext"`
}

func (h *Handler) createSessionToken(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)

	var req createSessionTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.ProviderName = strings.TrimSpace(req.ProviderName)
	req.Model = strings.TrimSpace(req.Model)
	req.Label = strings.TrimSpace(req.Label)
	if !providerNameRe.MatchString(req.ProviderName) {
		writeError(w, http.StatusBadRequest, "invalid provider_name")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}

	prov, err := h.repo.GetProviderByName(r.Context(), req.ProviderName)
	if err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "provider not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !modelAllowed(prov, req.Model) {
		writeError(w, http.StatusBadRequest, "model not allowed by provider")
		return
	}

	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid expires_at (RFC3339 required)")
			return
		}
		expiresAt = &t
	}

	created, err := h.repo.CreateSessionToken(r.Context(), prov.ID, req.Model, req.Label, caller.ID, expiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, createSessionTokenResp{
		Token:     toPublicSessionToken(created.Token, prov.Name),
		Plaintext: created.Plaintext,
	})
}

// modelAllowed enforces the per-provider model allow-list. An empty
// AllowedModels list falls back to the provider's DefaultModel so a
// minimally-configured provider still works without a separate "enabled
// everything" sentinel.
func modelAllowed(prov *domain.Provider, model string) bool {
	if len(prov.AllowedModels) == 0 {
		if prov.DefaultModel == "" {
			// Nothing configured: defer to the proxy / upstream to reject.
			return true
		}
		return model == prov.DefaultModel
	}
	for _, m := range prov.AllowedModels {
		if m == model {
			return true
		}
	}
	return false
}

func (h *Handler) listSessionTokens(w http.ResponseWriter, r *http.Request) {
	rows, err := h.repo.ListSessionTokens(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Resolve provider names in one pass; a small registry rarely has more
	// than a handful of providers so a per-row cache keeps the query count
	// at O(distinct providers).
	names := map[int64]string{}
	items := make([]publicSessionToken, 0, len(rows))
	for _, t := range rows {
		name, ok := names[t.ProviderID]
		if !ok {
			p, err := h.repo.GetProviderByID(r.Context(), t.ProviderID)
			if err == nil {
				name = p.Name
				names[t.ProviderID] = name
			}
		}
		items = append(items, toPublicSessionToken(t, name))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) revokeSessionToken(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if err := h.repo.RevokeSessionToken(r.Context(), id); err != nil {
		if errors.Is(err, domain.ErrTokenNotFound) {
			writeError(w, http.StatusNotFound, "session token not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- usage ----

type publicUsage struct {
	ID               int64     `json:"id"`
	SessionTokenID   *int64    `json:"session_token_id,omitempty"`
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
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		if n > maxUsageLimit {
			n = maxUsageLimit
		}
		limit = n
	}

	var providerID *int64
	if name := strings.TrimSpace(q.Get("provider")); name != "" {
		if !providerNameRe.MatchString(name) {
			writeError(w, http.StatusBadRequest, "invalid provider")
			return
		}
		p, err := h.repo.GetProviderByName(r.Context(), name)
		if err != nil {
			if errors.Is(err, domain.ErrProviderNotFound) {
				writeError(w, http.StatusNotFound, "provider not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		providerID = &p.ID
	}

	var since *time.Time
	if raw := strings.TrimSpace(q.Get("since")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since (RFC3339 required)")
			return
		}
		since = &t
	}

	rows, err := h.usage.ListUsage(r.Context(), providerID, since, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicUsage, 0, len(rows))
	for _, u := range rows {
		items = append(items, publicUsage{
			ID:               u.ID,
			SessionTokenID:   u.SessionTokenID,
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
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ---- helpers ----

func (h *Handler) loadProviderByName(w http.ResponseWriter, r *http.Request) (*domain.Provider, bool) {
	name := chi.URLParam(r, "name")
	if !providerNameRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid name")
		return nil, false
	}
	p, err := h.repo.GetProviderByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "provider not found")
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return p, true
}

func parseID(w http.ResponseWriter, raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
