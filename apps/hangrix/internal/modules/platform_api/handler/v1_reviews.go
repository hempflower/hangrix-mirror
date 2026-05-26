package handler

import (
	"encoding/json"
	"net/http"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
)

func v1CreateReview(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "reviews", "create") {
			return
		}
		var req struct {
			ContributionID int64  `json:"contribution_id"`
			Value          string `json:"value"`
			Reason         string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.ContributionID <= 0 {
			WriteFieldError(w, http.StatusUnprocessableEntity, "contribution_id is required",
				apidomain.FieldError{Field: "contribution_id", Code: "missing"},
			)
			return
		}
		if req.Value == "" {
			WriteFieldError(w, http.StatusUnprocessableEntity, "value is required (approve, reject, or abstain)",
				apidomain.FieldError{Field: "value", Code: "missing"},
			)
			return
		}
		result, err := api.CreateReview(r.Context(), p, req.ContributionID, req.Value, req.Reason)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteCreated(w, result)
	}
}
