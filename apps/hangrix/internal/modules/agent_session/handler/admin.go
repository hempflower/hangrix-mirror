// Package handler exposes the agent_session module's admin-only HTTP
// surface — the M7a audit query view promised in roadmap.md §M7a Phase 2.
// Mounted at /api/admin/agent-sessions; cookie + RequireAdmin gated. The
// route returns the (agent_sha, repo_sha, cause_id) snapshot triples that
// reconstruct who-did-what for any issue.
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
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
		r.Get("/by-issue/{repo_id}/{issue_number}", h.listByIssue)
	})
}

type publicAuditSession struct {
	SessionID  int64           `json:"session_id"`
	RunnerID   *int64          `json:"runner_id,omitempty"`
	RepoID     int64           `json:"repo_id"`
	Issue      int32           `json:"issue_number"`
	RoleKey    string          `json:"role_key"`
	Status     string          `json:"status"`
	AgentRepo  string          `json:"agent_repo"`
	AgentSHA   string          `json:"agent_sha"`
	RepoSHA    string          `json:"repo_sha"`
	CauseKind  string          `json:"cause_kind"`
	CauseID    string          `json:"cause_id"`
	RoleConfig json.RawMessage `json:"role_config"`
	CreatedAt  time.Time       `json:"created_at"`
	EndedAt    *time.Time      `json:"ended_at,omitempty"`
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
		items = append(items, publicAuditSession{
			SessionID:  r.SessionID,
			RunnerID:   r.RunnerID,
			RepoID:     r.RepoID,
			Issue:      r.Issue,
			RoleKey:    r.RoleKey,
			Status:     r.Status,
			AgentRepo:  r.AgentRepo,
			AgentSHA:   r.AgentSHA,
			RepoSHA:    r.RepoSHA,
			CauseKind:  r.CauseKind,
			CauseID:    r.CauseID,
			RoleConfig: r.RoleConfig,
			CreatedAt:  r.CreatedAt,
			EndedAt:    r.EndedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
