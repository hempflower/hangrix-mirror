// Package service hosts the stateless business logic for issue
// attachments: file validation (extension, MIME, size), SHA256
// computation, on-disk storage path management, and the orchestration
// between the domain.AttachmentStore and the filesystem.
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
)

// MaxAttachmentSize is the 64 MiB upload limit.
const MaxAttachmentSize = 64 << 20

// AllowedExtensions is the whitelist of file extensions accepted on
// upload. Extensions are lowercased (with and without leading dot)
// before matching.
var AllowedExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true,
	".mp4":  true, ".webm": true, ".mov": true,
	".zip": true, ".tar.gz": true, ".tgz": true, ".gz": true,
	".txt": true, ".md": true, ".json": true, ".yaml": true,
	".yml": true, ".log": true, ".csv": true, ".pdf": true,
	".bin": true,
}

// ErrAttachmentTooLarge is returned when the uploaded file exceeds
// MaxAttachmentSize.
var ErrAttachmentTooLarge = errors.New("attachment exceeds 64 MiB limit")

// ErrAttachmentExtension is returned when the file extension is not in
// the AllowedExtensions whitelist.
var ErrAttachmentExtension = errors.New("file type not allowed")

// AttachmentService is the composable business-logic layer that sits
// between the HTTP handler and the persistence store. It handles
// validation, hashing, and on-disk writes — all concerns the handler
// and infra layers should not need to know about.
type AttachmentService struct {
	store           domain.AttachmentStore
	attachmentsPath string
}

// AttachmentServiceDeps is the ioc-shaped input.
type AttachmentServiceDeps struct {
	Store  domain.AttachmentStore
	Config *config.Config
}

// NewAttachmentService wires the service with the store and the
// configured attachments directory.
func NewAttachmentService(deps *AttachmentServiceDeps) *AttachmentService {
	return &AttachmentService{
		store:           deps.Store,
		attachmentsPath: deps.Config.Storage.AttachmentsPath,
	}
}

// Upload validates the multipart file, computes its SHA256, writes it
// to disk under <attachments_path>/<repo_id>/<issue_id>/<random>/<sha256>,
// and creates a domain.Attachment row (status = uploaded).
func (s *AttachmentService) Upload(
	ctx context.Context,
	repoID, issueID, authorID int64,
	agentRole string,
	file multipart.File,
	header *multipart.FileHeader,
) (*domain.Attachment, error) {
	originalName := header.Filename
	ext := strings.ToLower(filepath.Ext(originalName))

	// Special-case .tar.gz / .tgz — filepath.Ext returns ".gz".
	switch {
	case strings.HasSuffix(strings.ToLower(originalName), ".tar.gz"):
		ext = ".tar.gz"
	case strings.HasSuffix(strings.ToLower(originalName), ".tgz"):
		ext = ".tgz"
	}

	if !AllowedExtensions[ext] {
		return nil, fmt.Errorf("%w: %s", ErrAttachmentExtension, ext)
	}

	if header.Size > MaxAttachmentSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrAttachmentTooLarge, header.Size)
	}

	// Detect MIME from the first 512 bytes.
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, fmt.Errorf("read header: %w", err)
	}
	head = head[:n]
	detectedMime := http.DetectContentType(head)

	// Ensure the root attachments directory exists before CreateTemp.
	if err := os.MkdirAll(s.attachmentsPath, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir attachments root: %w", err)
	}

	// Compute SHA256 over head + remainder while streaming to a temp file.
	hasher := sha256.New()
	hasher.Write(head)

	tmp, err := os.CreateTemp(s.attachmentsPath, "upload-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if _, err := tmp.Write(head); err != nil {
		return nil, fmt.Errorf("write temp head: %w", err)
	}
	written, err := io.Copy(io.MultiWriter(tmp, hasher), file)
	if err != nil {
		return nil, fmt.Errorf("write temp body: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return nil, fmt.Errorf("sync temp: %w", err)
	}

	totalBytes := int64(len(head)) + written
	sha256Sum := hex.EncodeToString(hasher.Sum(nil))

	// Determine kind from detected MIME.
	kind := kindFromMime(detectedMime)

	// Client-supplied MIME from the multipart header.
	clientMime := header.Header.Get("Content-Type")

	// Generate a random 8-char hex directory component so the storage
	// key doesn't depend on the auto-increment attachment ID. This
	// avoids a chicken-and-egg: we need the key before CreateAttachment,
	// but the ID only exists after the insert.
	dirComp, err := randHex(8)
	if err != nil {
		return nil, fmt.Errorf("generate dir: %w", err)
	}

	// storageKey = <repo_id>/<issue_id>/<dirComp>/<sha256>
	storageKey := filepath.Join(
		fmt.Sprintf("%d", repoID),
		fmt.Sprintf("%d", issueID),
		dirComp,
		sha256Sum,
	)

	// Create the on-disk target directory and move the temp file in.
	targetDir := filepath.Join(s.attachmentsPath, filepath.Dir(storageKey))
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir storage: %w", err)
	}
	targetPath := filepath.Join(s.attachmentsPath, storageKey)
	if err := os.Rename(tmp.Name(), targetPath); err != nil {
		return nil, fmt.Errorf("move to storage: %w", err)
	}

	// Now create the DB row with the storage key.
	attachment, err := s.store.CreateAttachment(ctx, repoID, issueID, authorID,
		"", agentRole, storageKey, originalName, "", totalBytes, clientMime, detectedMime, sha256Sum, kind, false)
	if err != nil {
		// Best-effort cleanup of the already-written file.
		_ = os.Remove(targetPath)
		return nil, fmt.Errorf("create attachment row: %w", err)
	}

	return attachment, nil
}

// UploadAttachment fulfills domain.AttachmentUploader. It accepts raw
// file bytes (sent by the agent_api tool after decoding base64 from
// the agent) together with metadata, validates the extension and size,
// computes SHA256, writes the file to disk, and creates the DB row.
// authorID is always 0 — agent uploads have no user author.
func (s *AttachmentService) UploadAttachment(
	ctx context.Context,
	params *domain.AttachmentUploadParams,
) (*domain.Attachment, error) {
	ext := strings.ToLower(filepath.Ext(params.Name))

	// Special-case .tar.gz / .tgz — filepath.Ext returns ".gz".
	switch {
	case strings.HasSuffix(strings.ToLower(params.Name), ".tar.gz"):
		ext = ".tar.gz"
	case strings.HasSuffix(strings.ToLower(params.Name), ".tgz"):
		ext = ".tgz"
	}

	if !AllowedExtensions[ext] {
		return nil, fmt.Errorf("%w: %s", ErrAttachmentExtension, ext)
	}

	if len(params.Data) > MaxAttachmentSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrAttachmentTooLarge, len(params.Data))
	}

	// Detect MIME from the first 512 bytes.
	head := params.Data
	if len(head) > 512 {
		head = head[:512]
	}
	detectedMime := http.DetectContentType(head)

	// Ensure the root attachments directory exists before CreateTemp.
	if err := os.MkdirAll(s.attachmentsPath, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir attachments root: %w", err)
	}

	// Compute SHA256 and write to a temp file.
	hasher := sha256.New()
	tmp, err := os.CreateTemp(s.attachmentsPath, "upload-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	// Write to both the hasher and the temp file in one pass.
	tee := io.MultiWriter(tmp, hasher)
	if _, err := tee.Write(params.Data); err != nil {
		return nil, fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return nil, fmt.Errorf("sync temp: %w", err)
	}

	sha256Sum := hex.EncodeToString(hasher.Sum(nil))
	kind := kindFromMime(detectedMime)

	// Client-supplied MIME — agent may pass application/octet-stream.
	clientMime := "application/octet-stream"

	// Generate a random 8-char hex directory component.
	dirComp, err := randHex(8)
	if err != nil {
		return nil, fmt.Errorf("generate dir: %w", err)
	}

	// storageKey = <repo_id>/<issue_id>/<dirComp>/<sha256>
	storageKey := filepath.Join(
		fmt.Sprintf("%d", params.RepoID),
		fmt.Sprintf("%d", params.IssueID),
		dirComp,
		sha256Sum,
	)

	// Create the on-disk target directory and move the temp file in.
	targetDir := filepath.Join(s.attachmentsPath, filepath.Dir(storageKey))
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir storage: %w", err)
	}
	targetPath := filepath.Join(s.attachmentsPath, storageKey)
	if err := os.Rename(tmp.Name(), targetPath); err != nil {
		return nil, fmt.Errorf("move to storage: %w", err)
	}

	// Create the DB row. authorID is always 0 for agent uploads.
	attachment, err := s.store.CreateAttachment(ctx, params.RepoID, params.IssueID, 0,
		"", params.AgentRole, storageKey, params.Name, params.DisplayName, int64(len(params.Data)),
		clientMime, detectedMime, sha256Sum, kind, params.Inline)
	if err != nil {
		_ = os.Remove(targetPath)
		return nil, fmt.Errorf("create attachment row: %w", err)
	}

	// If the caller specified a comment, transition directly to attached.
	if params.CommentID > 0 {
		if err := s.store.MarkAttached(ctx, attachment.ID, params.CommentID); err != nil {
			_ = os.Remove(targetPath)
			return nil, fmt.Errorf("mark attached: %w", err)
		}
		attachment.CommentID = params.CommentID
		attachment.Status = domain.AttachmentStatusAttached
	}
	return attachment, nil
}

// Get is a pass-through to the store.
func (s *AttachmentService) Get(ctx context.Context, id int64) (*domain.Attachment, error) {
	return s.store.GetAttachment(ctx, id)
}

// List is a pass-through to the store.
func (s *AttachmentService) List(ctx context.Context, issueID, commentID int64) ([]*domain.Attachment, error) {
	return s.store.ListAttachments(ctx, issueID, commentID)
}

// MarkAttached transitions an uploaded attachment to the attached
// state, linking it to the given comment.
func (s *AttachmentService) MarkAttached(ctx context.Context, id, commentID int64) error {
	return s.store.MarkAttached(ctx, id, commentID)
}

// SoftDelete soft-deletes the attachment row and removes the on-disk
// file. Missing files are not an error — the row is still tombstoned.
func (s *AttachmentService) SoftDelete(ctx context.Context, id int64) error {
	att, err := s.store.GetAttachment(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.SoftDeleteAttachment(ctx, id); err != nil {
		return err
	}
	// Remove the on-disk file — best-effort.
	diskPath := filepath.Join(s.attachmentsPath, att.StorageKey)
	_ = os.Remove(diskPath)
	// Clean up the parent directories if empty (best-effort).
	_ = os.Remove(filepath.Dir(diskPath))
	return nil
}

// ReadPath returns the absolute on-disk path for an attachment, or an
// error if the file does not exist. Used by the download handler.
func (s *AttachmentService) ReadPath(att *domain.Attachment) (string, error) {
	p := filepath.Join(s.attachmentsPath, att.StorageKey)
	if _, err := os.Stat(p); err != nil {
		return "", domain.ErrAttachmentNotFound
	}
	return p, nil
}

// --- helpers ---

// randHex returns n random hex characters (2n bytes of entropy).
func randHex(n int) (string, error) {
	buf := make([]byte, n/2+1)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf)[:n], nil
}

// kindFromMime maps a MIME type to an AttachmentKind for frontend
// rendering decisions.
func kindFromMime(mime string) domain.AttachmentKind {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return domain.AttachmentKindImage
	case strings.HasPrefix(mime, "video/"):
		return domain.AttachmentKindVideo
	case mime == "application/zip",
		mime == "application/gzip",
		mime == "application/x-tar",
		mime == "application/x-gzip":
		return domain.AttachmentKindArchive
	case strings.HasPrefix(mime, "text/"),
		mime == "application/json",
		mime == "application/x-yaml",
		mime == "application/pdf":
		return domain.AttachmentKindText
	default:
		return domain.AttachmentKindBinary
	}
}
