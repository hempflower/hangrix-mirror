package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	agentapidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_api/domain"
)

// ---- Issue read / edit ----

func v1ReadIssue(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		issue, err := api.ReadIssue(r.Context(), p)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		WriteOK(w, issue)
	}
}

func v1EditIssue(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		var req struct {
			Title *string `json:"title"`
			Body  *string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.Title == nil && req.Body == nil {
			WriteFieldError(w, http.StatusUnprocessableEntity, "at least one of title or body is required",
				agentapidomain.FieldError{Field: "title", Code: "missing"},
				agentapidomain.FieldError{Field: "body", Code: "missing"},
			)
			return
		}
		result, err := api.EditIssue(r.Context(), p, req.Title, req.Body)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}

// ---- Issue by number (cross-issue read) ----

func v1ReadIssueByNumber(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		// Support both forms:
		//   /issues/{issueNumber}           — uses actor scope repo
		//   /repos/{repoID}/issues/{issueNumber} — explicit repo (if both params present)
		issueNumber, ok := parseIDParam(w, chi.URLParam(r, "issueNumber"))
		if !ok {
			return
		}
		if repoIDStr := chi.URLParam(r, "repoID"); repoIDStr != "" {
			repoID, ok := parseIDParam(w, repoIDStr)
			if !ok {
				return
			}
			if p.RepoID == nil || *p.RepoID != repoID {
				WriteError(w, http.StatusForbidden, "cross-repo issue reads are not allowed")
				return
			}
		}
		// Must have repo scope.
		if !p.InRepo() {
			WriteError(w, http.StatusForbidden, "actor has no repo scope")
			return
		}
		issue, err := api.ReadIssueByNumber(r.Context(), p, issueNumber)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, issue)
	}
}

// ---- Issue create ----

func v1CreateIssue(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		repoID, ok := parseIDParam(w, chi.URLParam(r, "repoID"))
		if !ok {
			return
		}
		if p.RepoID == nil || *p.RepoID != repoID {
			WriteError(w, http.StatusForbidden, "cross-repo issue creation is not allowed")
			return
		}
		var req struct {
			Title  string `json:"title"`
			Body   string `json:"body"`
			Parent bool   `json:"parent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.Title == "" {
			WriteFieldError(w, http.StatusUnprocessableEntity, "title is required",
				agentapidomain.FieldError{Field: "title", Code: "missing"},
			)
			return
		}
		result, err := api.CreateIssue(r.Context(), p, req.Title, req.Body, req.Parent)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteCreated(w, result)
	}
}

// ---- Comments ----

func v1CreateComment(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		var req struct {
			Body     string `json:"body"`
			FilePath string `json:"file_path"`
			Line     int    `json:"line"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.Body == "" {
			WriteFieldError(w, http.StatusUnprocessableEntity, "body is required",
				agentapidomain.FieldError{Field: "body", Code: "missing"},
			)
			return
		}
		comment, err := api.CreateComment(r.Context(), p, req.Body, req.FilePath, req.Line)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteCreated(w, comment)
	}
}

func v1GetComment(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		commentID, ok := parseIDParam(w, chi.URLParam(r, "commentID"))
		if !ok {
			return
		}
		comment, err := api.GetComment(r.Context(), p, commentID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, comment)
	}
}

// ---- Helpers ----

// requireActor extracts the Principal from the request or writes a
// 401 and returns nil.
func requireActor(w http.ResponseWriter, r *http.Request) *agentapidomain.Actor {
	p := GetActor(r)
	if p == nil {
		WriteError(w, http.StatusUnauthorized, "missing bearer token")
		return nil
	}
	return p
}

// writeServiceError maps common service-layer errors to HTTP status codes.
func writeServiceError(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case containsAny(msg, "not found", "out of scope", "does not belong"):
		WriteError(w, http.StatusNotFound, msg)
	case containsAny(msg, "required", "invalid", "must be", "missing"):
		WriteError(w, http.StatusUnprocessableEntity, msg)
	case containsAny(msg, "already", "conflict", "cannot be closed", "cannot change"):
		WriteError(w, http.StatusConflict, msg)
	case containsAny(msg, "not granted", "forbidden", "cannot approve your own"):
		WriteError(w, http.StatusForbidden, msg)
	default:
		WriteError(w, http.StatusInternalServerError, msg)
	}
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
