package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
)

func v1CreateRelease(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "releases", "create") {
			return
		}
		var req struct {
			TagName string `json:"tag_name"`
			Title   string `json:"title"`
			Notes   string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.TagName == "" {
			WriteFieldError(w, http.StatusUnprocessableEntity, "tag_name is required",
				apidomain.FieldError{Field: "tag_name", Code: "missing"},
			)
			return
		}
		result, err := api.CreateRelease(r.Context(), p, req.TagName, req.Title, req.Notes)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteCreated(w, result)
	}
}

func v1UpdateRelease(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "releases", "update") {
			return
		}
		id, ok := parseIDParam(w, chi.URLParam(r, "id"))
		if !ok {
			return
		}
		var req struct {
			TagName *string `json:"tag_name"`
			Title   *string `json:"title"`
			Notes   *string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := api.UpdateRelease(r.Context(), p, id, req.TagName, req.Title, req.Notes)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}

func v1DeleteRelease(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "releases", "delete") {
			return
		}
		id, ok := parseIDParam(w, chi.URLParam(r, "id"))
		if !ok {
			return
		}
		if err := api.DeleteRelease(r.Context(), p, id); err != nil {
			writeServiceError(w, err)
			return
		}
		WriteNoContent(w)
	}
}

func v1PublishRelease(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "releases", "publish") {
			return
		}
		id, ok := parseIDParam(w, chi.URLParam(r, "id"))
		if !ok {
			return
		}
		result, err := api.PublishRelease(r.Context(), p, id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteOK(w, result)
	}
}

func v1UploadReleaseAsset(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "releases", "create") {
			return
		}
		id, ok := parseIDParam(w, chi.URLParam(r, "id"))
		if !ok {
			return
		}
		var req struct {
			Name        string `json:"name"`
			Content     string `json:"content"`
			ContentType string `json:"content_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.Name == "" {
			WriteFieldError(w, http.StatusUnprocessableEntity, "name is required",
				apidomain.FieldError{Field: "name", Code: "missing"},
			)
			return
		}
		if req.Content == "" {
			WriteFieldError(w, http.StatusUnprocessableEntity, "content is required",
				apidomain.FieldError{Field: "content", Code: "missing"},
			)
			return
		}
		result, err := api.UploadReleaseAsset(r.Context(), p, id, req.Name, req.Content, req.ContentType)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteCreated(w, result)
	}
}
