package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
)

// ---- Create Questionnaire ---- //

func v1CreateQuestionnaire(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "questionnaires", "create") {
			return
		}

		var input apidomain.CreateQuestionnaireInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		result, err := api.CreateQuestionnaire(r.Context(), p, input)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteCreated(w, result)
	}
}

// ---- List Questionnaires ---- //

func v1ListQuestionnaires(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "questionnaires", "list") {
			return
		}

		result, err := api.ListQuestionnaires(r.Context(), p)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}

// ---- Get Questionnaire ---- //

func v1GetQuestionnaire(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "questionnaires", "read") {
			return
		}

		id, ok := parseIDParam(w, chi.URLParam(r, "id"))
		if !ok {
			return
		}

		result, err := api.GetQuestionnaire(r.Context(), p, id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}

// ---- Get Questionnaire Result ---- //

func v1GetQuestionnaireResult(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "questionnaires", "read") {
			return
		}

		id, ok := parseIDParam(w, chi.URLParam(r, "id"))
		if !ok {
			return
		}

		result, err := api.GetQuestionnaireResult(r.Context(), p, id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}

// ---- Close Questionnaire ---- //

func v1CloseQuestionnaire(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "questionnaires", "update") {
			return
		}

		id, ok := parseIDParam(w, chi.URLParam(r, "id"))
		if !ok {
			return
		}

		var req struct {
			Reason string `json:"reason"`
		}
		// body is optional; empty reason is fine
		if r.Body != nil && r.ContentLength > 0 {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}

		result, err := api.CloseQuestionnaire(r.Context(), p, id, req.Reason)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}
