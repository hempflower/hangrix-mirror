// Package handler exposes the admin dashboard HTTP surface at
// GET /api/admin/dashboard.
package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/dashboard/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/dashboard/infra"
	llmproviderdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
)

type Handler struct {
	repo         *infra.PostgresRepo
	providerRepo llmproviderdomain.Repo
	middleware   authdomain.Middleware
}

type HandlerDeps struct {
	Repo         *infra.PostgresRepo
	ProviderRepo llmproviderdomain.Repo
	Middleware   authdomain.Middleware
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		repo:         deps.Repo,
		providerRepo: deps.ProviderRepo,
		middleware:   deps.Middleware,
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/admin/dashboard", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Use(h.middleware.RequireAdmin)
		r.Get("/", h.getDashboard)
	})
}

func (h *Handler) getDashboard(w http.ResponseWriter, r *http.Request) {
	filter, ok := parseFilter(w, r, h.providerRepo)
	if !ok {
		return
	}

	// Run all queries. They are independent reads against the same pool.
	summary, err := h.repo.SummaryStats(r.Context(), filter)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dashboard summary: "+err.Error())
		return
	}

	dailyCalls, err := h.repo.DailyCalls(r.Context(), filter)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dashboard daily_calls: "+err.Error())
		return
	}

	dailyTokens, err := h.repo.DailyTokens(r.Context(), filter)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dashboard daily_tokens: "+err.Error())
		return
	}

	providers, err := h.repo.ProviderBreakdown(r.Context(), filter)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dashboard providers: "+err.Error())
		return
	}

	health, err := h.repo.RunnerHealth(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dashboard health: "+err.Error())
		return
	}

	totalRunners, err := h.repo.TotalRunners(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dashboard total_runners: "+err.Error())
		return
	}

	recentFailures, err := h.repo.RecentFailures(r.Context(), filter, 10)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dashboard recent_failures: "+err.Error())
		return
	}

	// Ensure empty slices instead of null in JSON output.
	if dailyCalls == nil {
		dailyCalls = []domain.DailyCalls{}
	}
	if dailyTokens == nil {
		dailyTokens = []domain.DailyTokens{}
	}
	if providers == nil {
		providers = []domain.ProviderStat{}
	}
	if recentFailures == nil {
		recentFailures = []domain.FailureItem{}
	}

	summary.ActiveSessions = health.LiveSessions
	summary.OnlineRunners = health.OnlineRunners
	summary.TotalRunners = totalRunners

	resp := domain.DashboardResponse{
		Summary: summary,
		Timeseries: domain.Timeseries{
			DailyCalls:  dailyCalls,
			DailyTokens: dailyTokens,
		},
		Providers:      providers,
		Health:         health,
		RecentFailures: recentFailures,
	}

	httpx.WriteJSON(w, http.StatusOK, resp)
}

// parseFilter extracts since, until, and optional provider from query params.
// On error it writes the 400 response and returns ok=false.
func parseFilter(w http.ResponseWriter, r *http.Request, providerRepo llmproviderdomain.Repo) (infra.DashboardFilter, bool) {
	q := r.URL.Query()
	f := infra.DashboardFilter{}

	if raw := strings.TrimSpace(q.Get("since")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid since (RFC3339 required)")
			return f, false
		}
		f.Since = &t
	}
	if raw := strings.TrimSpace(q.Get("until")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid until (RFC3339 required)")
			return f, false
		}
		f.Until = &t
	}

	if name := strings.TrimSpace(q.Get("provider")); name != "" {
		p, err := providerRepo.GetProviderByName(r.Context(), name)
		if err != nil {
			httpx.WriteError(w, http.StatusNotFound, "provider not found")
			return f, false
		}
		f.ProviderID = &p.ID
	}

	return f, true
}
