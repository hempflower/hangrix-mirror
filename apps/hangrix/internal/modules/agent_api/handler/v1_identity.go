package handler

import (
	"net/http"
)

// v1Me returns the authenticated agent's identity.
func v1Me(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := GetActor(r)
		if p == nil {
			WriteError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		me, err := api.GetMe(r.Context(), p)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		WriteOK(w, me)
	}
}
