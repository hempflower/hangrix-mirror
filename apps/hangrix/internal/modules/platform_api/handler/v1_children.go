package handler

import (
	"net/http"
)

func v1ListChildren(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "issues", "read") {
			return
		}
		items, err := api.ListChildren(r.Context(), p)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, items)
	}
}

func v1ListChecks(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "issues", "read") {
			return
		}
		items, err := api.ListChecks(r.Context(), p)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, items)
	}
}
