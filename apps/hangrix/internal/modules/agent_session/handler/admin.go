// Package handler exposes the agent_session module's admin-only HTTP
// surface — the audit query view plus container lifecycle controls.
// Mounted at /api/admin/agent-sessions; cookie + RequireAdmin gated.
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
	controller domain.Controller
	middleware authdomain.Middleware
}

type AdminHandlerDeps struct {
	Auditor    domain.Auditor
	Controller domain.Controller
	Middleware authdomain.Middleware
}

func NewAdminHandler(deps *AdminHandlerDeps) *AdminHandler {
	return &AdminHandler{
		auditor:    deps.Auditor,
		controller: deps.Controller,
		middleware: deps.Middleware,
	}
}

func (h *AdminHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/admin/agent-sessions", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Use(h.middleware.RequireAdmin)
		r.Get("/", h.listRecent)
		r.Get("/by-issue/{repo_id}/{issue_number}", h.listByIssue)
		r.Post("/{id}/stop-container", h.stopContainer)
		r.Post("/{id}/remove-container", h.removeContainer)
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
	if v := strings.TrimSpace(q.Get("offset")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid offset")
			return
		}
		opts.Offset = n
	}
	rows, total, err := h.auditor.ListRecent(r.Context(), opts)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicAuditSession, 0, len(rows))
	for _, r := range rows {
		items = append(items, toPublicAuditSession(r))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
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

	// Container lifecycle.
	ContainerID             string     `json:"container_id,omitempty"`
	ContainerLastUsedAt     *time.Time `json:"container_last_used_at,omitempty"`
	ContainerStoppedAt      *time.Time `json:"container_stopped_at,omitempty"`
	ContainerStopPending    bool       `json:"container_stop_pending"`
	ContainerCleanupPending bool       `json:"container_cleanup_pending"`
	RunningJobs             int32      `json:"running_jobs"`
}

func toPublicAuditSession(r domain.AuditSession) publicAuditSession {
	return publicAuditSession{
		SessionID:              r.SessionID,
		RunnerID:               r.RunnerID,
		RepoID:                 r.RepoID,
		Issue:                  r.Issue,
		RoleKey:                r.RoleKey,
		Status:                 r.Status,
		RepoSHA:                r.RepoSHA,
		CauseKind:              r.CauseKind,
		CauseID:                r.CauseID,
		RoleConfig:             r.RoleConfig,
		ExitCode:               r.ExitCode,
		ErrorMessage:           r.ErrorMessage,
		CreatedAt:              r.CreatedAt,
		EndedAt:                r.EndedAt,
		ContainerID:            r.ContainerID,
		ContainerLastUsedAt:    r.ContainerLastUsedAt,
		ContainerStoppedAt:     r.ContainerStoppedAt,
		ContainerStopPending:   r.ContainerStopPending,
		ContainerCleanupPending: r.ContainerCleanupPending,
		RunningJobs:            r.RunningJobs,
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

// stopContainer flags the session's container for an immediate docker
// stop by the owning runner. POST /api/admin/agent-sessions/{id}/stop-container
func (h *AdminHandler) stopContainer(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if err := h.controller.StopContainerNow(r.Context(), id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removeContainer flags the session's container for an immediate docker
// rm by the owning runner. POST /api/admin/agent-sessions/{id}/remove-container
func (h *AdminHandler) removeContainer(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if err := h.controller.RemoveContainerNow(r.Context(), id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
