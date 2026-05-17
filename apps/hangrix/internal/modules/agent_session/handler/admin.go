// Package handler exposes the agent_session module's admin-only HTTP
// surface — the audit query view. Mounted at /api/admin/agent-sessions;
// cookie + RequireAdmin gated. The route returns the (repo_sha,
// cause_kind, cause_id, role_config) snapshot tuples that reconstruct
// who-did-what for any issue.
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
)

type AdminHandler struct {
	auditor    domain.Auditor
	middleware authdomain.Middleware
}

type AdminHandlerDeps struct {
	Auditor    domain.Auditor
	Middleware authdomain.Middleware
}

func NewAdminHandler(deps *AdminHandlerDeps) *AdminHandler {
	return &AdminHandler{
		auditor:    deps.Auditor,
		middleware: deps.Middleware,
	}
}

func (h *AdminHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/admin/agent-sessions", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Use(h.middleware.RequireAdmin)
		r.Get("/", h.listRecent)
		r.Get("/by-issue/{repo_id}/{issue_number}", h.listByIssue)
	})
}

// listRecent powers the admin global audit page. Filter params are all
// optional: role_key, status, repo_id, since (RFC3339), limit (capped at
// 500 server-side). With no filters returns the latest `limit` rows
// platform-wide.
func (h *AdminHandler) listRecent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := domain.RecentFilter{Limit: 100}
	if v := strings.TrimSpace(q.Get("role_key")); v != "" {
		opts.RoleKey = &v
	}
	if v := strings.TrimSpace(q.Get("status")); v != "" {
		opts.Status = &v
	}
	if v := strings.TrimSpace(q.Get("repo_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid repo_id")
			return
		}
		opts.RepoID = &id
	}
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid since (RFC3339 required)")
			return
		}
		opts.Since = &t
	}
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		if n > 500 {
			n = 500
		}
		opts.Limit = n
	}
	rows, err := h.auditor.ListRecent(r.Context(), opts)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicAuditSession, 0, len(rows))
	for _, r := range rows {
		items = append(items, toPublicAuditSession(r))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type publicAuditSession struct {
	SessionID    int64           `json:"session_id"`
	RunnerID     *int64          `json:"runner_id,omitempty"`
	RepoID       int64           `json:"repo_id"`
	Issue        int32           `json:"issue_number"`
	RoleKey      string          `json:"role_key"`
	Status       string          `json:"status"`
	RepoSHA      string          `json:"repo_sha"`
	CauseKind    string          `json:"cause_kind"`
	CauseID      string          `json:"cause_id"`
	RoleConfig   json.RawMessage `json:"role_config"`
	ExitCode     *int32          `json:"exit_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	EndedAt      *time.Time      `json:"ended_at,omitempty"`
}

func toPublicAuditSession(r domain.AuditSession) publicAuditSession {
	return publicAuditSession{
		SessionID:    r.SessionID,
		RunnerID:     r.RunnerID,
		RepoID:       r.RepoID,
		Issue:        r.Issue,
		RoleKey:      r.RoleKey,
		Status:       r.Status,
		RepoSHA:      r.RepoSHA,
		CauseKind:    r.CauseKind,
		CauseID:      r.CauseID,
		RoleConfig:   r.RoleConfig,
		ExitCode:     r.ExitCode,
		ErrorMessage: r.ErrorMessage,
		CreatedAt:    r.CreatedAt,
		EndedAt:      r.EndedAt,
	}
}

func (h *AdminHandler) listByIssue(w http.ResponseWriter, r *http.Request) {
	repoID, err := strconv.ParseInt(chi.URLParam(r, "repo_id"), 10, 64)
	if err != nil || repoID <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid repo_id")
		return
	}
	issueNumber, err := strconv.ParseInt(chi.URLParam(r, "issue_number"), 10, 32)
	if err != nil || issueNumber <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid issue_number")
		return
	}
	rows, err := h.auditor.ListByIssue(r.Context(), repoID, int32(issueNumber))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicAuditSession, 0, len(rows))
	for _, r := range rows {
		items = append(items, toPublicAuditSession(r))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
