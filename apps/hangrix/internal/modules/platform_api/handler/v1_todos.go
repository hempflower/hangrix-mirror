package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
)

func v1ListTodos(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "todos", "list") {
			return
		}
		result, err := api.ListTodos(r.Context(), p)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}

func v1CreateTodo(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "todos", "update") {
			return
		}
		var req struct {
			Content  string `json:"content"`
			Status   string `json:"status"`
			Position int    `json:"position"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.Content == "" {
			WriteFieldError(w, http.StatusUnprocessableEntity, "content is required",
				apidomain.FieldError{Field: "content", Code: "missing"},
			)
			return
		}
		todo, err := api.CreateTodo(r.Context(), p, req.Content, req.Status, req.Position)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteCreated(w, todo)
	}
}

func v1UpdateTodo(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "todos", "update") {
			return
		}
		todoID, ok := parseIDParam(w, chi.URLParam(r, "todoID"))
		if !ok {
			return
		}
		var req struct {
			Status  string  `json:"status"`
			Content *string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		todo, err := api.UpdateTodo(r.Context(), p, todoID, req.Status, req.Content)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, todo)
	}
}
