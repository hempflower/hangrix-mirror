package handler

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
)

func v1UploadAttachment(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := requireActor(w, r)
		if p == nil {
			return
		}
		if !requirePermission(w, p, "attachments", "create") {
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			WriteError(w, http.StatusUnsupportedMediaType, "content-type must be multipart/form-data")
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			WriteError(w, http.StatusBadRequest, "parse multipart form: "+err.Error())
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			WriteFieldError(w, http.StatusUnprocessableEntity, "missing or invalid 'file' part",
				apidomain.FieldError{Field: "file", Code: "missing"},
			)
			return
		}
		defer file.Close()

		fileBytes, err := io.ReadAll(file)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "read file: "+err.Error())
			return
		}

		name := header.Filename
		displayName := strings.TrimSpace(r.FormValue("display_name"))
		inline := strings.TrimSpace(r.FormValue("inline")) == "true"
		commentID := int64(0)
		if raw := strings.TrimSpace(r.FormValue("comment_id")); raw != "" {
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
				commentID = n
			}
		}

		result, err := api.UploadAttachment(r.Context(), p, fileBytes, name, displayName, inline, commentID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteCreated(w, result)
	}
}
