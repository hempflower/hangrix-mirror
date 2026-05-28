// Package domain defines the platform-level attachment model, its lifecycle
// states, and the persistence interfaces consumed by the service and handler
// layers. Unlike the issue-scoped attachments (issue_attachments table in the
// issue module), these attachments are stored at the platform level and
// referenced from comments via native Markdown URLs.
package domain

import (
	"context"
	"errors"
	"time"
)

// AttachmentKind classifies the file for frontend rendering decisions.
type AttachmentKind string

const (
	AttachmentKindImage   AttachmentKind = "image"
	AttachmentKindVideo   AttachmentKind = "video"
	AttachmentKindArchive AttachmentKind = "archive"
	AttachmentKindText    AttachmentKind = "text"
	AttachmentKindBinary  AttachmentKind = "binary"
)

// AttachmentStatus tracks the lifecycle of an attachment row.
type AttachmentStatus string

const (
	AttachmentStatusUploaded AttachmentStatus = "uploaded"
	AttachmentStatusAttached AttachmentStatus = "attached"
	AttachmentStatusDeleted  AttachmentStatus = "deleted"
)

// ErrAttachmentNotFound is returned when a lookup by ID finds no row.
var ErrAttachmentNotFound = errors.New("attachment not found")

// Attachment is the domain model for a platform-level uploaded file.
// Rows start as "uploaded" and transition to "attached" when a comment
// body references the attachment via a native Markdown URL. Soft-delete
// sets status=deleted and wipes the on-disk file.
type Attachment struct {
	ID               int64
	StorageKey       string
	OriginalName     string
	DisplayName      string
	SizeBytes        int64
	MimeType         string
	DetectedMimeType string
	SHA256           string
	Kind             AttachmentKind
	Inline           bool
	Status           AttachmentStatus
	ActorID          int64  // FK to actors(id); replaces author_id+agent_role
	CreatedAt        time.Time
	DeletedAt        *time.Time
}

// CommentAttachment links a comment to a platform-level attachment.
type CommentAttachment struct {
	CommentID    int64
	AttachmentID int64
}

// Store is the persistence abstraction for platform-level attachments.
type Store interface {
	CreateAttachment(ctx context.Context, actorID int64, storageKey, originalName, displayName string, sizeBytes int64, mimeType, detectedMimeType, sha256 string, kind AttachmentKind, inline bool) (*Attachment, error)
	GetAttachment(ctx context.Context, id int64) (*Attachment, error)
	SoftDeleteAttachment(ctx context.Context, id int64) error
}

// CommentAttachmentStore tracks which attachments are referenced by which
// comments. When a comment body contains a native Markdown URL pointing to
// /api/attachments/{id}, the comment handler creates a row here.
type CommentAttachmentStore interface {
	CreateCommentAttachment(ctx context.Context, commentID, attachmentID int64) error
	ListAttachmentIDsByComment(ctx context.Context, commentID int64) ([]int64, error)
}

// AttachmentUploadParams carries the data the agent_api tool passes when
// uploading an attachment on behalf of an agent session. Data is the raw
// file bytes (decoded from base64 on the server side).
type AttachmentUploadParams struct {
	Data        []byte // raw file bytes
	Name        string // original filename (e.g. "screenshot.png")
	DisplayName string // optional display name override
	Inline      bool
	CommentID   int64
	AgentRole   string
	ActorID     int64 // resolved actor_id — fast path when the caller already resolved it
}

// Uploader is the cross-module seam for uploading attachments from the
// agent_api tool.
type Uploader interface {
	Upload(ctx context.Context, params *AttachmentUploadParams) (*Attachment, error)
}
