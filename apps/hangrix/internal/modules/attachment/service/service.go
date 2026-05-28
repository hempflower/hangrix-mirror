// Package service hosts the stateless business logic for platform-level
// attachments: file validation (extension, MIME, size), SHA256 computation,
// on-disk storage path management, and the orchestration between the
// domain.Store and the filesystem.
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

	"github.com/hangrix/hangrix/pkg/actor"

	actordomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment/domain"
)

// MaxAttachmentSize is the 64 MiB upload limit.
const MaxAttachmentSize = 64 << 20

// AllowedExtensions is the whitelist of file extensions accepted on upload.
var AllowedExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true,
	".mp4":  true, ".webm": true, ".mov": true,
	".zip": true, ".tar.gz": true, ".tgz": true, ".gz": true,
	".txt": true, ".md": true, ".json": true, ".yaml": true,
	".yml": true, ".log": true, ".csv": true, ".pdf": true,
	".bin": true,
}

// ErrAttachmentTooLarge is returned when the uploaded file exceeds MaxAttachmentSize.
var ErrAttachmentTooLarge = errors.New("attachment exceeds 64 MiB limit")

// ErrAttachmentExtension is returned when the file extension is not in the whitelist.
var ErrAttachmentExtension = errors.New("file type not allowed")

// Service is the composable business-logic layer that sits between the HTTP
// handler and the persistence store.
type Service struct {
	store           domain.Store
	attachmentsPath string
	actorResolver   actordomain.Resolver
}

// ServiceDeps is the ioc-shaped input.
type ServiceDeps struct {
	Store         domain.Store
	Config        *config.Config
	ActorResolver actordomain.Resolver
}

// NewService wires the service with the store and the configured attachments
// directory.
func NewService(deps *ServiceDeps) *Service {
	return &Service{
		store:           deps.Store,
		attachmentsPath: deps.Config.Storage.AttachmentsPath,
		actorResolver:   deps.ActorResolver,
	}
}

// UploadMultipart validates a multipart file, computes its SHA256, writes it
// to disk under <attachments_path>/<dirComp>/<sha256>, and creates a
// domain.Attachment row (status = uploaded).
func (s *Service) UploadMultipart(
	ctx context.Context,
	actorID int64,
	displayName string,
	inline bool,
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

	// Ensure the root attachments directory exists.
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
	kind := kindFromMime(detectedMime)
	clientMime := header.Header.Get("Content-Type")

	// Generate a random 8-char hex directory component.
	dirComp, err := randHex(8)
	if err != nil {
		return nil, fmt.Errorf("generate dir: %w", err)
	}

	// storageKey = <dirComp>/<sha256>
	storageKey := filepath.Join(dirComp, sha256Sum)

	// Create the on-disk target directory and move the temp file in.
	targetDir := filepath.Join(s.attachmentsPath, filepath.Dir(storageKey))
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir storage: %w", err)
	}
	targetPath := filepath.Join(s.attachmentsPath, storageKey)
	if err := os.Rename(tmp.Name(), targetPath); err != nil {
		return nil, fmt.Errorf("move to storage: %w", err)
	}

	if displayName == "" {
		displayName = originalName
	}

	attachment, err := s.store.CreateAttachment(ctx, actorID,
		storageKey, originalName, displayName, totalBytes, clientMime, detectedMime, sha256Sum, kind, inline)
	if err != nil {
		_ = os.Remove(targetPath)
		return nil, fmt.Errorf("create attachment row: %w", err)
	}

	return attachment, nil
}

// Upload accepts raw file bytes (from the agent_api tool or any cross-module
// caller), validates, hashes, writes to disk, and creates the DB row. It
// implements domain.Uploader.
func (s *Service) Upload(
	ctx context.Context,
	params *domain.AttachmentUploadParams,
) (*domain.Attachment, error) {
	ext := strings.ToLower(filepath.Ext(params.Name))

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

	tee := io.MultiWriter(tmp, hasher)
	if _, err := tee.Write(params.Data); err != nil {
		return nil, fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return nil, fmt.Errorf("sync temp: %w", err)
	}

	sha256Sum := hex.EncodeToString(hasher.Sum(nil))
	kind := kindFromMime(detectedMime)
	clientMime := "application/octet-stream"

	dirComp, err := randHex(8)
	if err != nil {
		return nil, fmt.Errorf("generate dir: %w", err)
	}

	storageKey := filepath.Join(dirComp, sha256Sum)

	targetDir := filepath.Join(s.attachmentsPath, filepath.Dir(storageKey))
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir storage: %w", err)
	}
	targetPath := filepath.Join(s.attachmentsPath, storageKey)
	if err := os.Rename(tmp.Name(), targetPath); err != nil {
		return nil, fmt.Errorf("move to storage: %w", err)
	}

	displayName := params.DisplayName
	if displayName == "" {
		displayName = params.Name
	}

	actorID := int64(0)
	if params.ActorID > 0 {
		actorID = params.ActorID
	} else if params.AgentRole != "" && s.actorResolver != nil {
		resolved, err := s.actorResolver.From(ctx, actor.AgentRef(params.AgentRole))
		if err == nil {
			actorID = resolved.ActorID
		}
	}
	if actorID == 0 {
		actorID = 1 // fallback to system actor
	}
	attachment, err := s.store.CreateAttachment(ctx, actorID,
		storageKey, params.Name, displayName, int64(len(params.Data)),
		clientMime, detectedMime, sha256Sum, kind, params.Inline)
	if err != nil {
		_ = os.Remove(targetPath)
		return nil, fmt.Errorf("create attachment row: %w", err)
	}

	return attachment, nil
}

// Get is a pass-through to the store.
func (s *Service) Get(ctx context.Context, id int64) (*domain.Attachment, error) {
	return s.store.GetAttachment(ctx, id)
}

// SoftDelete soft-deletes the attachment row and removes the on-disk file.
func (s *Service) SoftDelete(ctx context.Context, id int64) error {
	att, err := s.store.GetAttachment(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.SoftDeleteAttachment(ctx, id); err != nil {
		return err
	}
	diskPath := filepath.Join(s.attachmentsPath, att.StorageKey)
	_ = os.Remove(diskPath)
	_ = os.Remove(filepath.Dir(diskPath))
	return nil
}

// ReadPath returns the absolute on-disk path for an attachment.
func (s *Service) ReadPath(att *domain.Attachment) (string, error) {
	p := filepath.Join(s.attachmentsPath, att.StorageKey)
	if _, err := os.Stat(p); err != nil {
		return "", domain.ErrAttachmentNotFound
	}
	return p, nil
}

// --- helpers ---

func randHex(n int) (string, error) {
	buf := make([]byte, n/2+1)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf)[:n], nil
}

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
