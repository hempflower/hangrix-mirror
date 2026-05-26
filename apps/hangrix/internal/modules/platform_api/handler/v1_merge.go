package handler

import (
	"encoding/json"
	"net/http"
)

func v1Mergeability(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "issues", "mergeability") {
			return
		}
		result, err := api.GetMergeability(r.Context(), p)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}

func v1MergeIssue(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "issues", "merge") {
			return
		}
		var req struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // body optional
		result, err := api.MergeIssue(r.Context(), p, req.Message)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}

func v1CloseIssue(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "issues", "close") {
			return
		}
		var req struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // body optional
		result, err := api.CloseIssue(r.Context(), p, req.Reason)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}
