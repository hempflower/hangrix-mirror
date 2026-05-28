// Package handler exposes the platform-level attachment HTTP surface:
//
//	POST   /api/attachments            — multipart file upload
//	GET    /api/attachments/{id}/download — serve file
//	DELETE /api/attachments/{id}        — soft-delete
//
// All routes require authentication. The handler also implements
// server.RouteProvider so the chi router picks it up via ioc.
package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/pkg/actor"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	actordomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment/service"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	workflowdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
)

// Handler is the HTTP handler for platform-level attachments.
type Handler struct {
	svc              *service.Service
	middleware       authdomain.Middleware
	wfTokenValidator workflowdomain.WorkflowTokenValidator
	actorResolver    actordomain.Resolver
}

// HandlerDeps is the ioc-shaped constructor input.
type HandlerDeps struct {
	Service          *service.Service
	Middleware       authdomain.Middleware
	WfTokenValidator workflowdomain.WorkflowTokenValidator
	ActorResolver    actordomain.Resolver
}

// NewHandler creates the handler.
func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		svc:              deps.Service,
		middleware:       deps.Middleware,
		wfTokenValidator: deps.WfTokenValidator,
		actorResolver:    deps.ActorResolver,
	}
}

// RegisterRoutes mounts the platform-level attachment routes on the chi router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/attachments", func(r chi.Router) {
		r.Use(h.authGate)
		r.Post("/", h.create)
		r.Get("/{id}/download", h.download)
		r.Delete("/{id}", h.delete)
	})
}

// create handles multipart file upload.
// POST /api/attachments
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	// Workflow-authenticated callers are allowed (authorID=0, agentRole="workflow").
	_, isWF := actor.WorkflowActorFromRequest(r)
	caller, _ := authdomain.UserFromRequest(r)
	if caller == nil && !isWF {
		httpx.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, service.MaxAttachmentSize+1<<10)

	if err := r.ParseMultipartForm(service.MaxAttachmentSize); err != nil {
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

	displayName := r.FormValue("display_name")
	inline := r.FormValue("inline") == "true"

	// Resolve actor_id via actor.Resolver. For workflow uploads the actor
	// Ref was stored in request context by authGate; for human callers we
	// resolve user_id → actor_id via EnsureUser.
	actorID := int64(0)
	if isWF {
		wfActor, _ := actor.WorkflowActorFromRequest(r)
		if h.actorResolver != nil {
			resolved, err := h.actorResolver.From(r.Context(), wfActor)
			if err == nil {
				actorID = resolved.ActorID
			}
		}
		if actorID == 0 {
			actorID = 1 // fallback to system actor
		}
	} else if caller != nil && h.actorResolver != nil {
		resolved, err := h.actorResolver.From(r.Context(), actor.UserRef(caller.ID, ""))
		if err == nil {
			actorID = resolved.ActorID
		}
	}
	attachment, err := h.svc.UploadMultipart(r.Context(), actorID, displayName, inline, file, header)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAttachmentTooLarge):
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, err.Error())
		case errors.Is(err, service.ErrAttachmentExtension):
			httpx.WriteError(w, http.StatusUnsupportedMediaType, err.Error())
		default:
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, toPublicAttachment(attachment))
}

// download streams an attachment file to the client.
// GET /api/attachments/{id}/download
func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	att, err := h.svc.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrAttachmentNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "attachment not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if att.Status == domain.AttachmentStatusDeleted {
		httpx.WriteError(w, http.StatusGone, "attachment deleted")
		return
	}

	diskPath, err := h.svc.ReadPath(att)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "attachment file not found")
		return
	}

	w.Header().Set("Content-Type", att.DetectedMimeType)
	w.Header().Set("Content-Disposition", "inline; filename=\""+att.OriginalName+"\"")
	http.ServeFile(w, r, diskPath)
}

// delete soft-deletes an attachment.
// DELETE /api/attachments/{id}
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)
	if caller == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	id, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	att, err := h.svc.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrAttachmentNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "attachment not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Only the uploader can delete. Resolve the caller's user_id → actor_id
	// and compare against the attachment's actor_id. System-actor attachments
	// (ActorID == 0) can be deleted by any authenticated user.
	if att.ActorID != 0 {
		callerActorID := int64(0)
		if h.actorResolver != nil {
			resolved, err := h.actorResolver.From(r.Context(), actor.UserRef(caller.ID, ""))
			if err == nil {
				callerActorID = resolved.ActorID
			}
		}
		if callerActorID != att.ActorID {
			httpx.WriteError(w, http.StatusForbidden, "forbidden")
			return
		}
	}

	if err := h.svc.SoftDelete(r.Context(), id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- DTO ---

type publicAttachment struct {
	ID               int64   `json:"id"`
	OriginalName     string  `json:"original_name"`
	DisplayName      string  `json:"display_name"`
	SizeBytes        int64   `json:"size_bytes"`
	MimeType         string  `json:"mime_type"`
	DetectedMimeType string  `json:"detected_mime_type"`
	SHA256           string  `json:"sha256"`
	Kind             string  `json:"kind"`
	Inline           bool    `json:"inline"`
	Status           string  `json:"status"`
	AuthorID         int64   `json:"author_id"`
	AgentRole        string  `json:"agent_role,omitempty"`
	URL              string  `json:"url"`
	MarkdownSnippet  string  `json:"markdown_snippet"`
	CreatedAt        string  `json:"created_at"`
	DeletedAt        *string `json:"deleted_at,omitempty"`
}

func toPublicAttachment(a *domain.Attachment) publicAttachment {
	downloadURL := fmt.Sprintf("/api/attachments/%d/download", a.ID)
	var markdownSnippet string
	if a.Inline && (a.Kind == domain.AttachmentKindImage || a.Kind == domain.AttachmentKindVideo) {
		markdownSnippet = fmt.Sprintf("![](/api/attachments/%d/download)", a.ID)
	} else {
		name := a.DisplayName
		if name == "" {
			name = a.OriginalName
		}
		markdownSnippet = fmt.Sprintf("[%s](/api/attachments/%d/download)", name, a.ID)
	}

	out := publicAttachment{
		ID:               a.ID,
		OriginalName:     a.OriginalName,
		DisplayName:      a.DisplayName,
		SizeBytes:        a.SizeBytes,
		MimeType:         a.MimeType,
		DetectedMimeType: a.DetectedMimeType,
		SHA256:           a.SHA256,
		Kind:             string(a.Kind),
		Inline:           a.Inline,
		Status:           string(a.Status),
		AuthorID:         a.ActorID,
		URL:              downloadURL,
		MarkdownSnippet:  markdownSnippet,
		CreatedAt:        a.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if a.DeletedAt != nil {
		s := a.DeletedAt.UTC().Format("2006-01-02T15:04:05Z")
		out.DeletedAt = &s
	}
	return out
}

// authGate is a middleware that tries workflow token auth first, then falls
// back to cookie-based session auth. This allows workflow containers to
// upload and download attachments via a Bearer token.
func (h *Handler) authGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Try workflow token (Bearer hangrix_wf_...).
		if h.wfTokenValidator != nil {
			tok, err := bearerTokenFromHeader(r)
			if err == nil && strings.HasPrefix(tok, "hangrix_wf_") {
				_, wfActor, err := h.wfTokenValidator.ValidateWorkflowTokenWithActor(r.Context(), tok)
				if err == nil {
					r = actor.WithWorkflowActor(r, wfActor)
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		// 2. Fall back to cookie auth.
		h.middleware.RequireAuth(next).ServeHTTP(w, r)
	})
}

// bearerTokenFromHeader extracts the Bearer token from the Authorization header.
func bearerTokenFromHeader(r *http.Request) (string, error) {
	hdr := r.Header.Get("Authorization")
	if hdr == "" {
		return "", errors.New("missing authorization header")
	}
	const pfx = "Bearer "
	if !strings.HasPrefix(hdr, pfx) {
		return "", errors.New("authorization must be Bearer")
	}
	tok := strings.TrimSpace(hdr[len(pfx):])
	if tok == "" {
		return "", errors.New("empty bearer token")
	}
	return tok, nil
}
