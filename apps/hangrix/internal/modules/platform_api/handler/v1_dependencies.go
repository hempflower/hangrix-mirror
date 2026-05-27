package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func v1AddDependency(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "issues", "write") {
			return
		}
		var req struct {
			DependsOn int64 `json:"depends_on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.DependsOn <= 0 {
			WriteError(w, http.StatusBadRequest, "depends_on is required")
			return
		}
		result, err := api.AddDependency(r.Context(), p, req.DependsOn)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}

func v1RemoveDependency(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "issues", "write") {
			return
		}
		dependsOn, err := strconv.ParseInt(chi.URLParam(r, "dependsOnNumber"), 10, 64)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "invalid depends_on number")
			return
		}
		result, err := api.RemoveDependency(r.Context(), p, dependsOn)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}

func v1ReadDependencies(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "issues", "read") {
			return
		}
		result, err := api.ReadDependencies(r.Context(), p)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}
