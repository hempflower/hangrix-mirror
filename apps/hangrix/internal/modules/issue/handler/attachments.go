package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/pkg/actor"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	issueservice "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/service"
)

// createAttachment handles multipart file upload to an issue.
// POST /api/repos/{owner}/{name}/issues/{number}/attachments
func (h *Handler) createAttachment(w http.ResponseWriter, r *http.Request) {
	if h.attachments == nil {
		httpx.WriteError(w, http.StatusNotImplemented, "attachments not configured")
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}
	caller, _ := authdomain.UserFromRequest(r)
	if caller == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	// 64 MiB max — reject early before parsing multipart body.
	r.Body = http.MaxBytesReader(w, r.Body, issueservice.MaxAttachmentSize+1<<10)

	if err := r.ParseMultipartForm(issueservice.MaxAttachmentSize); err != nil {
		if errors.Is(err, http.ErrNotMultipart) || errors.Is(err, http.ErrMissingBoundary) {
			httpx.WriteError(w, http.StatusBadRequest, "multipart form required")
			return
		}
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "missing 'file' field")
		return
	}
	defer file.Close()

	attachment, err := h.attachments.Upload(r.Context(), rc.repo.ID, iss.ID, caller.ID, "", file, header)
	if err != nil {
		switch {
		case errors.Is(err, issueservice.ErrAttachmentTooLarge):
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, err.Error())
		case errors.Is(err, issueservice.ErrAttachmentExtension):
			httpx.WriteError(w, http.StatusUnsupportedMediaType, err.Error())
		default:
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, toPublicAttachment(rc.repo.OwnerName, rc.repo.Name, iss.Number, attachment))
}

// listAttachments lists attachments for an issue.
// GET /api/repos/{owner}/{name}/issues/{number}/attachments?comment_id=
func (h *Handler) listAttachments(w http.ResponseWriter, r *http.Request) {
	if h.attachments == nil {
		httpx.WriteError(w, http.StatusNotImplemented, "attachments not configured")
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}

	var commentID int64
	if raw := r.URL.Query().Get("comment_id"); raw != "" {
		var err error
		commentID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || commentID <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid comment_id")
			return
		}
	}

	attachments, err := h.attachments.List(r.Context(), iss.ID, commentID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]publicAttachment, 0, len(attachments))
	for _, a := range attachments {
		out = append(out, toPublicAttachment(rc.repo.OwnerName, rc.repo.Name, iss.Number, a))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// getAttachment streams an attachment file to the client.
// GET /api/repos/{owner}/{name}/issues/{number}/attachments/{id}
func (h *Handler) getAttachment(w http.ResponseWriter, r *http.Request) {
	if h.attachments == nil {
		httpx.WriteError(w, http.StatusNotImplemented, "attachments not configured")
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}

	id, ok := parseAttachmentID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	att, err := h.attachments.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrAttachmentNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "attachment not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Verify the attachment belongs to the resolved repo and issue.
	if att.RepoID != rc.repo.ID || att.IssueID != iss.ID {
		httpx.WriteError(w, http.StatusNotFound, "attachment not found")
		return
	}
	if att.Status == domain.AttachmentStatusDeleted {
		httpx.WriteError(w, http.StatusGone, "attachment deleted")
		return
	}

	diskPath, err := h.attachments.ReadPath(att)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "attachment file not found")
		return
	}

	w.Header().Set("Content-Type", att.DetectedMimeType)
	w.Header().Set("Content-Disposition", "inline; filename=\""+att.OriginalName+"\"")
	http.ServeFile(w, r, diskPath)
}

// deleteAttachment soft-deletes an attachment.
// DELETE /api/repos/{owner}/{name}/issues/{number}/attachments/{id}
func (h *Handler) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	if h.attachments == nil {
		httpx.WriteError(w, http.StatusNotImplemented, "attachments not configured")
		return
	}
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	iss, ok := h.loadIssue(w, r, rc.repo.ID)
	if !ok {
		return
	}

	caller, _ := authdomain.UserFromRequest(r)
	if caller == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	id, ok := parseAttachmentID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	att, err := h.attachments.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrAttachmentNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "attachment not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Verify the attachment belongs to the resolved repo and issue.
	if att.RepoID != rc.repo.ID || att.IssueID != iss.ID {
		httpx.WriteError(w, http.StatusNotFound, "attachment not found")
		return
	}

	// Only the uploader or a repo manager can delete.
	// Agent-uploaded attachments (AuthorID == 0) have no human author,
	// so any authenticated user who can access the repo may delete them.
	if att.AuthorID != 0 && caller.ID != att.AuthorID && !h.canManage(r, caller, rc.repo) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	if err := h.attachments.SoftDelete(r.Context(), id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- DTOs ---

type publicAttachment struct {
	ID               int64      `json:"id"`
	RepoID           int64      `json:"repo_id"`
	IssueID          int64      `json:"issue_id"`
	CommentID        int64      `json:"comment_id"`
	AuthorID         int64      `json:"author_id"`
	AgentRole        string     `json:"agent_role,omitempty"`
	Actor            *actor.Ref `json:"actor,omitempty"`
	OriginalName     string     `json:"original_name"`
	DisplayName      string     `json:"display_name"`
	SizeBytes        int64      `json:"size_bytes"`
	MimeType         string     `json:"mime_type"`
	DetectedMimeType string     `json:"detected_mime_type"`
	SHA256           string     `json:"sha256"`
	Kind             string     `json:"kind"`
	Inline           bool       `json:"inline"`
	Status           string     `json:"status"`
	DownloadURL      string     `json:"download_url"`
	PreviewURL       string     `json:"preview_url"`
	MarkdownSnippet  string     `json:"markdown_snippet"`
	CreatedAt        time.Time  `json:"created_at"`
	DeletedAt        *time.Time `json:"deleted_at,omitempty"`
}

func toPublicAttachment(owner, repoName string, issueNumber int64, a *domain.Attachment) publicAttachment {
	downloadURL := fmt.Sprintf("/api/repos/%s/%s/issues/%d/attachments/%d",
		owner, repoName, issueNumber, a.ID)
	var markdownSnippet string
	switch a.Kind {
	case domain.AttachmentKindImage, domain.AttachmentKindVideo:
		markdownSnippet = fmt.Sprintf("![attachment:%d]", a.ID)
	default:
		markdownSnippet = fmt.Sprintf("[attachment:%d]", a.ID)
	}
	var act *actor.Ref
	if !a.Actor.IsZero() {
		ref := a.Actor
		act = &ref
	}
	out := publicAttachment{
		ID:               a.ID,
		RepoID:           a.RepoID,
		IssueID:          a.IssueID,
		CommentID:        a.CommentID,
		AuthorID:         a.AuthorID,
		AgentRole:        a.AgentRole,
		Actor:            act,
		OriginalName:     a.OriginalName,
		DisplayName:      a.DisplayName,
		SizeBytes:        a.SizeBytes,
		MimeType:         a.MimeType,
		DetectedMimeType: a.DetectedMimeType,
		SHA256:           a.SHA256,
		Kind:             string(a.Kind),
		Inline:           a.Inline,
		Status:           string(a.Status),
		DownloadURL:      downloadURL,
		PreviewURL:       downloadURL,
		MarkdownSnippet:  markdownSnippet,
		CreatedAt:        a.CreatedAt,
		DeletedAt:        a.DeletedAt,
	}
	return out
}

func parseAttachmentID(w http.ResponseWriter, raw string) (int64, bool) {
	return httpx.ParseID(w, raw)
}
