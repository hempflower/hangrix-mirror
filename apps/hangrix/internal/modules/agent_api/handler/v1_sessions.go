package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func v1ListSessions(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		items, err := api.ListSessions(r.Context(), p)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, items)
	}
}

func v1RecoverSession(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		sessionID, ok := parseIDParam(w, chi.URLParam(r, "sessionID"))
		if !ok {
			return
		}
		result, err := api.RecoverSession(r.Context(), p, sessionID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}
