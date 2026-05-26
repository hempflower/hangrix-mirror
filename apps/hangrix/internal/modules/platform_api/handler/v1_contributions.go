package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
)

func v1ListContributions(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "contributions", "list") {
			return
		}
		includeClosed := parseBoolQuery(r, "include_closed")
		includeMerged := parseBoolQuery(r, "include_merged")
		items, err := api.ListContributions(r.Context(), p, includeClosed, includeMerged)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, items)
	}
}

func v1ReadContribution(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "contributions", "read") {
			return
		}
		id, ok := parseIDParam(w, chi.URLParam(r, "id"))
		if !ok {
			return
		}
		detail, err := api.ReadContribution(r.Context(), p, id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, detail)
	}
}

func v1SetContributionMeta(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "contributions", "set_meta") {
			return
		}
		id, ok := parseIDParam(w, chi.URLParam(r, "id"))
		if !ok {
			return
		}
		var req struct {
			Title       string `json:"title"`
			Description string `json:"description"`
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
		item, err := api.SetContributionMeta(r.Context(), p, id, req.Title, req.Description)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, item)
	}
}

func v1ApplyContribution(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "contributions", "apply") {
			return
		}
		id, ok := parseIDParam(w, chi.URLParam(r, "id"))
		if !ok {
			return
		}
		var req struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // body optional
		result, err := api.ApplyContribution(r.Context(), p, id, req.Message)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}

func v1CloseContribution(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "contributions", "close") {
			return
		}
		id, ok := parseIDParam(w, chi.URLParam(r, "id"))
		if !ok {
			return
		}
		var req struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // body optional
		result, err := api.CloseContribution(r.Context(), p, id, req.Reason)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}
