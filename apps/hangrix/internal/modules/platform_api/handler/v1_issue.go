package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
)

// ---- Issue read / edit ----

func v1ReadIssue(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "issues", "read") {
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
		if !requirePermission(w, p, "issues", "edit") {
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
				apidomain.FieldError{Field: "title", Code: "missing"},
				apidomain.FieldError{Field: "body", Code: "missing"},
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
		if !requirePermission(w, p, "issues", "read") {
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
		if !requirePermission(w, p, "issues", "create") {
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
				apidomain.FieldError{Field: "title", Code: "missing"},
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

// v1CreateCrossIssueComment handles POST /api/v1/issues/{issueNumber}/comments.
// Same request/response schema as v1CreateComment but the target issue is
// specified via URL path rather than the session's current issue. Only
// parent↔child issue pairs are allowed.
func v1CreateCrossIssueComment(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "comments", "create") {
			return
		}
		targetIssueNumber, ok := parseIDParam(w, chi.URLParam(r, "issueNumber"))
		if !ok {
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
				apidomain.FieldError{Field: "body", Code: "missing"},
			)
			return
		}
		if err := domain.ValidateCommentBody(req.Body); err != nil {
			var tooLong *domain.ErrCommentBodyTooLong
			if errors.As(err, &tooLong) {
				splitHint := fmt.Sprintf(
					"body has %d Unicode characters; the maximum is %d. "+
						"Split the content into multiple `issue_comment_cross` calls, "+
						"each ≤%d characters. "+
						"Prefix each segment with `[1/N]`, `[2/N]`, … so readers can follow the sequence.",
					tooLong.Runes, tooLong.Limit, tooLong.Limit,
				)
				WriteFieldError(w, http.StatusUnprocessableEntity,
					fmt.Sprintf("comment body too long: %d runes (limit %d)", tooLong.Runes, tooLong.Limit),
					apidomain.FieldError{Resource: "comment", Field: "body", Code: "too_long", Message: splitHint},
				)
				return
			}
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		comment, err := api.CreateCrossIssueComment(r.Context(), p, targetIssueNumber, req.Body, req.FilePath, req.Line)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteCreated(w, comment)
	}
}

func v1CreateComment(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "comments", "create") {
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
				apidomain.FieldError{Field: "body", Code: "missing"},
			)
			return
		}
		if err := domain.ValidateCommentBody(req.Body); err != nil {
			var tooLong *domain.ErrCommentBodyTooLong
			if errors.As(err, &tooLong) {
				splitHint := fmt.Sprintf(
					"body has %d Unicode characters; the maximum is %d. "+
						"Split the content into multiple `issue_comment` calls, "+
						"each ≤%d characters. "+
						"Prefix each segment with `[1/N]`, `[2/N]`, … so readers can follow the sequence.",
					tooLong.Runes, tooLong.Limit, tooLong.Limit,
				)
				WriteFieldError(w, http.StatusUnprocessableEntity,
					fmt.Sprintf("comment body too long: %d runes (limit %d)", tooLong.Runes, tooLong.Limit),
					apidomain.FieldError{Resource: "comment", Field: "body", Code: "too_long", Message: splitHint},
				)
				return
			}
			WriteError(w, http.StatusInternalServerError, err.Error())
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
		if !requirePermission(w, p, "comments", "read") {
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

// requireActor extracts the Actor from the request or writes a
// 401 and returns nil.
func requireActor(w http.ResponseWriter, r *http.Request) *apidomain.Actor {
	p := GetActor(r)
	if p == nil {
		WriteError(w, http.StatusUnauthorized, "missing bearer token")
		return nil
	}
	return p
}

// requirePermission checks whether the Actor's Permissions allow the
// given resource/action. Writes 403 and returns false when denied.
func requirePermission(w http.ResponseWriter, p *apidomain.Actor, resource, action string) bool {
	if p.Permissions == nil || !p.Permissions.Can(resource, action) {
		WriteError(w, http.StatusForbidden, "role \""+p.RoleKey+"\" lacks write permission for this operation (host yaml `permission:`)")
		return false
	}
	return true
}

// writeServiceError maps common service-layer errors to HTTP status codes.
func writeServiceError(w http.ResponseWriter, err error) {
	// BlockError carries structured precondition-failure data (code,
	// sub_issues) that the agent tool layer needs in the response so the
	// LLM can decide what to do next — emit a 409 matching the chi-handler
	// shape instead of falling through to the default 500.
	var be *domain.BlockError
	if errors.As(err, &be) {
		WriteJSON(w, http.StatusConflict, map[string]any{
			"error":        be.Message,
			"code":         be.Code,
			"block_reason": be.Message,
			"sub_issues":   be.SubIssues,
		})
		return
	}
	msg := err.Error()
	switch {
	case containsAny(msg, "not found", "out of scope", "does not belong"):
		WriteError(w, http.StatusNotFound, msg)
	case containsAny(msg, "required", "invalid", "must be", "missing", "too_long", "too_many", "too_few", "not_allowed", "duplicate", "validation"):
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
