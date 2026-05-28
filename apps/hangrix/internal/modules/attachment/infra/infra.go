// Package infra holds the Postgres-backed implementation of the attachment
// domain. SQL lives in queries.sql; sqlc generates the typed accessors under
// attachmentdb/. This file owns row → domain mapping.
package infra

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment/infra/attachmentdb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore implements domain.Store and domain.CommentAttachmentStore
// backed by sqlc-generated queries.
type PostgresStore struct {
	q    *attachmentdb.Queries
	pool *pgxpool.Pool
}

type PostgresStoreDeps struct {
	Pool *pgxpool.Pool
}

func NewPostgresStore(deps *PostgresStoreDeps) *PostgresStore {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("attachment migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_attachment", "."); err != nil {
		panic(fmt.Errorf("apply attachment migrations: %w", err))
	}
	return &PostgresStore{
		q:    attachmentdb.New(deps.Pool),
		pool: deps.Pool,
	}
}

// --- domain.Store ---

func (s *PostgresStore) CreateAttachment(ctx context.Context, actorID int64, storageKey, originalName, displayName string, sizeBytes int64, mimeType, detectedMimeType, sha256 string, kind domain.AttachmentKind, inline bool) (*domain.Attachment, error) {
	row, err := s.q.CreateAttachment(ctx, attachmentdb.CreateAttachmentParams{
		StorageKey:       storageKey,
		OriginalName:     originalName,
		DisplayName:      displayName,
		SizeBytes:        sizeBytes,
		MimeType:         mimeType,
		DetectedMimeType: detectedMimeType,
		Sha256:           sha256,
		Kind:             string(kind),
		Inline:           inline,
		Status:           string(domain.AttachmentStatusUploaded),
		ActorID:          actorID,
		
	})
	if err != nil {
		return nil, fmt.Errorf("create attachment: %w", err)
	}
	return s.GetAttachment(ctx, row.ID)
}

func (s *PostgresStore) GetAttachment(ctx context.Context, id int64) (*domain.Attachment, error) {
	row, err := s.q.GetAttachment(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrAttachmentNotFound
		}
		return nil, err
	}
	return attachmentFromRow(row), nil
}

func (s *PostgresStore) SoftDeleteAttachment(ctx context.Context, id int64) error {
	return s.q.SoftDeleteAttachment(ctx, id)
}

// --- domain.CommentAttachmentStore ---

func (s *PostgresStore) CreateCommentAttachment(ctx context.Context, commentID, attachmentID int64) error {
	return s.q.CreateCommentAttachment(ctx, attachmentdb.CreateCommentAttachmentParams{
		CommentID:    commentID,
		AttachmentID: attachmentID,
	})
}

func (s *PostgresStore) ListAttachmentIDsByComment(ctx context.Context, commentID int64) ([]int64, error) {
	rows, err := s.q.ListAttachmentIDsByComment(ctx, commentID)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	return out, nil
}

// --- row → domain ---

func attachmentFromRow(r attachmentdb.GetAttachmentRow) *domain.Attachment {
	a := &domain.Attachment{
		ID:               r.ID,
		StorageKey:       r.StorageKey,
		OriginalName:     r.OriginalName,
		DisplayName:      r.DisplayName,
		SizeBytes:        r.SizeBytes,
		MimeType:         r.MimeType,
		DetectedMimeType: r.DetectedMimeType,
		SHA256:           r.Sha256,
		Kind:             domain.AttachmentKind(r.Kind),
		Inline:           r.Inline,
		Status:           domain.AttachmentStatus(r.Status),
		ActorID:          r.ActorID,
		
		CreatedAt:        r.CreatedAt.Time,
	}
	if r.DeletedAt.Valid {
		t := r.DeletedAt.Time
		a.DeletedAt = &t
	}
	return a
}

// Ensure PostgresStore implements both interfaces.
var _ domain.Store = (*PostgresStore)(nil)
var _ domain.CommentAttachmentStore = (*PostgresStore)(nil)
