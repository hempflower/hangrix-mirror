// Package handler exposes the Personal Access Token HTTP surface mounted at
// /api/me/tokens. Every route is RequireAuth-gated; the caller is identified
// via authdomain.UserFromRequest. The plaintext token is returned only once,
// on the POST response — never again.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/domain"
)

type Handler struct {
	store      domain.Store
	middleware authdomain.Middleware
}

type HandlerDeps struct {
	Store      domain.Store
	Middleware authdomain.Middleware
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{store: deps.Store, middleware: deps.Middleware}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/me/tokens", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Delete("/{id}", h.revoke)
	})
}

// publicToken is the JSON shape returned to the owner. The hashed key and the
// plaintext are NEVER part of this struct — Plaintext rides on the
// create-response wrapper, not on the token row.
type publicToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

func toPublic(t *domain.Token) publicToken {
	scopes := make([]string, 0, len(t.Scopes))
	for _, s := range t.Scopes {
		scopes = append(scopes, string(s))
	}
	return publicToken{
		ID:         t.ID,
		Name:       t.Name,
		Prefix:     t.Prefix,
		Scopes:     scopes,
		LastUsedAt: t.LastUsedAt,
		ExpiresAt:  t.ExpiresAt,
		RevokedAt:  t.RevokedAt,
		CreatedAt:  t.CreatedAt,
	}
}

type createReq struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresAt *string  `json:"expires_at,omitempty"`
}

type createResp struct {
	Token     publicToken `json:"token"`
	Plaintext string      `json:"plaintext"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)

	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	scopes := make([]domain.Scope, 0, len(req.Scopes))
	for _, s := range req.Scopes {
		scopes = append(scopes, domain.Scope(s))
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

	created, err := h.store.Create(r.Context(), caller.ID, req.Name, scopes, expiresAt)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidName):
			writeError(w, http.StatusBadRequest, "invalid name (1-64 chars)")
		case errors.Is(err, domain.ErrInvalidScope):
			writeError(w, http.StatusBadRequest, "invalid scope")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusCreated, createResp{
		Token:     toPublic(created.Token),
		Plaintext: created.Plaintext,
	})
}

type listResp struct {
	Items []publicToken `json:"items"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)
	rows, err := h.store.ListByUser(r.Context(), caller.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicToken, 0, len(rows))
	for _, t := range rows {
		items = append(items, toPublic(t))
	}
	writeJSON(w, http.StatusOK, listResp{Items: items})
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)
	id, ok := parseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if err := h.store.Revoke(r.Context(), id, caller.ID); err != nil {
		if errors.Is(err, domain.ErrTokenNotFound) {
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
