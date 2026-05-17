// Package handler — agent session inspector routes.
//
// Two read-only endpoints surfaced under the issue path so the agents
// tab on the issue detail page can render per-role agent activity:
//
//	GET /api/repos/{owner}/{name}/issues/{n}/agent-sessions
//	GET /api/repos/{owner}/{name}/issues/{n}/agent-sessions/{sid}/messages
//
// Both share resolveRepo + loadIssue, so visibility (public repo /
// owner / org-member / admin) and the issue's existence are checked
// identically to the rest of the issue API.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	agentsessiondomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// publicAgentSession is the DTO returned by the sessions list endpoint.
// Mirrors the admin audit shape (snapshot pins, cause, role config)
// minus the session-token columns that never leave the server.
type publicAgentSession struct {
	SessionID    int64           `json:"session_id"`
	RunnerID     *int64          `json:"runner_id,omitempty"`
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

// publicAgentMessage is one row of an agent session's message log.
type publicAgentMessage struct {
	ID         int64           `json:"id"`
	Seq        int32           `json:"seq"`
	Kind       string          `json:"kind"`
	Role       string          `json:"role,omitempty"`
	Content    string          `json:"content,omitempty"`
	EventName  string          `json:"event,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  time.Time       `json:"created_at"`
}

func (h *Handler) listAgentSessions(w http.ResponseWriter, r *http.Request) {
	if h.auditor == nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": []publicAgentSession{}})
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	rows, err := h.auditor.ListByIssue(r.Context(), rc.repo.ID, int32(iss.Number))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicAgentSession, 0, len(rows))
	for _, s := range rows {
		items = append(items, publicAgentSession{
			SessionID:    s.SessionID,
			RunnerID:     s.RunnerID,
			RoleKey:      s.RoleKey,
			Status:       s.Status,
			RepoSHA:      s.RepoSHA,
			CauseKind:    s.CauseKind,
			CauseID:      s.CauseID,
			RoleConfig:   s.RoleConfig,
			ExitCode:     s.ExitCode,
			ErrorMessage: s.ErrorMessage,
			CreatedAt:    s.CreatedAt,
			EndedAt:      s.EndedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) listAgentSessionMessages(w http.ResponseWriter, r *http.Request) {
	if h.auditor == nil {
		httpx.WriteError(w, http.StatusNotFound, "agent session not found")
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	sid, err := strconv.ParseInt(chi.URLParam(r, "sid"), 10, 64)
	if err != nil || sid <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	// Scope guard: the session must belong to this (repo, issue). Without
	// this check, a reader of repo A could enumerate session message
	// logs from repo B by guessing session_ids.
	sess, err := h.auditor.GetSession(r.Context(), sid)
	if err != nil {
		if errors.Is(err, agentsessiondomain.ErrSessionNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "agent session not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sess.RepoID != rc.repo.ID || sess.Issue != int32(iss.Number) {
		httpx.WriteError(w, http.StatusNotFound, "agent session not found")
		return
	}
	msgs, err := h.auditor.ListMessages(r.Context(), sid)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicAgentMessage, 0, len(msgs))
	for _, m := range msgs {
		items = append(items, publicAgentMessage{
			ID:         m.ID,
			Seq:        m.Seq,
			Kind:       m.Kind,
			Role:       m.Role,
			Content:    m.Content,
			EventName:  m.EventName,
			ToolCallID: m.ToolCallID,
			ToolName:   m.ToolName,
			Payload:    m.Payload,
			CreatedAt:  m.CreatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// resolveSession is the per-(repo, issue, sid) authorisation +
// existence check shared by stop/resume/delete. Returns the validated
// session id and 'ok' — false means an error response has already
// been written.
func (h *Handler) resolveSession(w http.ResponseWriter, r *http.Request, requireManage bool) (int64, bool) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return 0, false
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return 0, false
	}
	if requireManage {
		caller, _ := authdomain.UserFromRequest(r)
		if !h.canManage(r, caller, rc.repo) && caller.ID != iss.AuthorID {
			httpx.WriteError(w, http.StatusForbidden, "only the repo owner or issue author can manage agent sessions")
			return 0, false
		}
	}
	sid, err := strconv.ParseInt(chi.URLParam(r, "sid"), 10, 64)
	if err != nil || sid <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid session id")
		return 0, false
	}
	if h.auditor == nil {
		httpx.WriteError(w, http.StatusNotFound, "agent session not found")
		return 0, false
	}
	sess, err := h.auditor.GetSession(r.Context(), sid)
	if err != nil {
		if errors.Is(err, agentsessiondomain.ErrSessionNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "agent session not found")
			return 0, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return 0, false
	}
	if sess.RepoID != rc.repo.ID || sess.Issue != int32(iss.Number) {
		httpx.WriteError(w, http.StatusNotFound, "agent session not found")
		return 0, false
	}
	return sid, true
}

type stopAgentSessionReq struct {
	Reason string `json:"reason,omitempty"`
}

func (h *Handler) stopAgentSession(w http.ResponseWriter, r *http.Request) {
	sid, ok := h.resolveSession(w, r, true)
	if !ok {
		return
	}
	if h.controller == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "agent session controls unavailable")
		return
	}
	var req stopAgentSessionReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := h.controller.Stop(r.Context(), sid, req.Reason); err != nil {
		if errors.Is(err, runnerdomain.ErrSessionNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "agent session not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) resumeAgentSession(w http.ResponseWriter, r *http.Request) {
	sid, ok := h.resolveSession(w, r, true)
	if !ok {
		return
	}
	if h.controller == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "agent session controls unavailable")
		return
	}
	if err := h.controller.Resume(r.Context(), sid); err != nil {
		switch {
		case errors.Is(err, runnerdomain.ErrSessionNotFound):
			httpx.WriteError(w, http.StatusNotFound, "agent session not found")
		case errors.Is(err, agentsessiondomain.ErrNotResumable):
			httpx.WriteError(w, http.StatusConflict, "session is not in a resumable state")
		default:
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteAgentSession(w http.ResponseWriter, r *http.Request) {
	sid, ok := h.resolveSession(w, r, true)
	if !ok {
		return
	}
	if h.controller == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "agent session controls unavailable")
		return
	}
	if err := h.controller.Delete(r.Context(), sid); err != nil {
		switch {
		case errors.Is(err, runnerdomain.ErrSessionNotFound):
			httpx.WriteError(w, http.StatusNotFound, "agent session not found")
		case errors.Is(err, agentsessiondomain.ErrSessionLive):
			httpx.WriteError(w, http.StatusConflict, "session is still live; stop it first")
		default:
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
