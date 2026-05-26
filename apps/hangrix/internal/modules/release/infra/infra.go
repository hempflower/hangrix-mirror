// Package infra holds the Postgres-backed implementations of the
// release domain's Store and AssetStore, plus a filesystem helper
// for storing custom asset binaries.
package infra

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/pkg/actor"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/release/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/release/infra/releasedb"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore implements domain.Store on top of a pgx pool.
type PostgresStore struct {
	q *releasedb.Queries
}

type PostgresStoreDeps struct {
	Pool *pgxpool.Pool
	// Resolver is wired purely for migration ordering: release migrations
	// FK repos(id), so repo migrations must run first.
	Resolver repodomain.PathResolver
}

func NewPostgresStore(deps *PostgresStoreDeps) *PostgresStore {
	_ = deps.Resolver // migration ordering guard
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("release migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_release", "."); err != nil {
		panic(fmt.Errorf("apply release migrations: %w", err))
	}
	return &PostgresStore{q: releasedb.New(deps.Pool)}
}

func (s *PostgresStore) Create(ctx context.Context, repoID int64, tagName, targetCommitSHA, title, notes string, createdActor actor.Ref) (*domain.Release, error) {
	var createdUserID pgtype.Int8
	if createdActor.UserID > 0 {
		createdUserID = pgtype.Int8{Int64: createdActor.UserID, Valid: true}
	}
	var createdWfID pgtype.Int8
	if createdActor.WorkflowRunID > 0 {
		createdWfID = pgtype.Int8{Int64: createdActor.WorkflowRunID, Valid: true}
	}
	row, err := s.q.CreateRelease(ctx, releasedb.CreateReleaseParams{
		RepoID:                      repoID,
		TagName:                     tagName,
		TargetCommitSha:             targetCommitSHA,
		Title:                       title,
		Notes:                       notes,
		IsDraft:                     true,
		CreatedActorKind:            string(createdActor.Kind),
		CreatedActorUserID:          createdUserID,
		CreatedActorRoleKey:         createdActor.RoleKey,
		CreatedActorWorkflowRunID:   createdWfID,
		CreatedActorDisplayName:     createdActor.DisplayName,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrReleaseConflict
		}
		return nil, err
	}
	return rowToRelease(row), nil
}

func (s *PostgresStore) GetByID(ctx context.Context, id int64) (*domain.Release, error) {
	row, err := s.q.GetReleaseByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrReleaseNotFound
		}
		return nil, err
	}
	return rowToRelease(row), nil
}

func (s *PostgresStore) GetByRepoAndTag(ctx context.Context, repoID int64, tagName string) (*domain.Release, error) {
	row, err := s.q.GetReleaseByRepoAndTag(ctx, releasedb.GetReleaseByRepoAndTagParams{
		RepoID:  repoID,
		TagName: tagName,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrReleaseNotFound
		}
		return nil, err
	}
	return rowToRelease(row), nil
}

func (s *PostgresStore) ListByRepo(ctx context.Context, repoID int64, offset, limit int32, draft *bool) ([]*domain.Release, int64, error) {
	if draft == nil {
		rows, err := s.q.ListReleasesByRepo(ctx, releasedb.ListReleasesByRepoParams{
			RepoID: repoID,
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			return nil, 0, err
		}
		total, err := s.q.CountReleasesByRepo(ctx, repoID)
		if err != nil {
			return nil, 0, err
		}
		out := make([]*domain.Release, 0, len(rows))
		for _, r := range rows {
			out = append(out, rowToRelease(r))
		}
		return out, total, nil
	}

	rows, err := s.q.ListReleasesByRepoDraft(ctx, releasedb.ListReleasesByRepoDraftParams{
		RepoID:  repoID,
		IsDraft: *draft,
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		return nil, 0, err
	}
	total, err := s.q.CountReleasesByRepoDraft(ctx, releasedb.CountReleasesByRepoDraftParams{
		RepoID:  repoID,
		IsDraft: *draft,
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]*domain.Release, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToRelease(r))
	}
	return out, total, nil
}

func (s *PostgresStore) Update(ctx context.Context, id int64, tagName, targetCommitSHA, title, notes string) (*domain.Release, error) {
	row, err := s.q.UpdateRelease(ctx, releasedb.UpdateReleaseParams{
		ID:              id,
		TagName:         tagName,
		TargetCommitSha: targetCommitSHA,
		Title:           title,
		Notes:           notes,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrReleaseNotFound
		}
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrReleaseConflict
		}
		return nil, err
	}
	return rowToRelease(row), nil
}

func (s *PostgresStore) Publish(ctx context.Context, id int64, publishedActor actor.Ref) (*domain.Release, error) {
	var pubUserID pgtype.Int8
	if publishedActor.UserID > 0 {
		pubUserID = pgtype.Int8{Int64: publishedActor.UserID, Valid: true}
	}
	var pubWfID pgtype.Int8
	if publishedActor.WorkflowRunID > 0 {
		pubWfID = pgtype.Int8{Int64: publishedActor.WorkflowRunID, Valid: true}
	}
	row, err := s.q.PublishRelease(ctx, releasedb.PublishReleaseParams{
		ID:                            id,
		PublishedActorKind:            string(publishedActor.Kind),
		PublishedActorUserID:          pubUserID,
		PublishedActorRoleKey:         publishedActor.RoleKey,
		PublishedActorWorkflowRunID:   pubWfID,
		PublishedActorDisplayName:     publishedActor.DisplayName,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrReleaseNotDraft
		}
		return nil, err
	}
	return rowToRelease(row), nil
}

func (s *PostgresStore) Delete(ctx context.Context, id int64) error {
	n, err := s.q.DeleteRelease(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrReleaseNotFound
	}
	return nil
}

// PostgresAssetStore implements domain.AssetStore.
type PostgresAssetStore struct {
	q *releasedb.Queries
}

type PostgresAssetStoreDeps struct {
	Pool *pgxpool.Pool
}

func NewPostgresAssetStore(deps *PostgresAssetStoreDeps) *PostgresAssetStore {
	return &PostgresAssetStore{q: releasedb.New(deps.Pool)}
}

func (s *PostgresAssetStore) Create(ctx context.Context, releaseID int64, name, contentType string, sizeBytes int64, storageKey string, uploadedActor actor.Ref) (*domain.Asset, error) {
	var upUserID pgtype.Int8
	if uploadedActor.UserID > 0 {
		upUserID = pgtype.Int8{Int64: uploadedActor.UserID, Valid: true}
	}
	var upWfID pgtype.Int8
	if uploadedActor.WorkflowRunID > 0 {
		upWfID = pgtype.Int8{Int64: uploadedActor.WorkflowRunID, Valid: true}
	}
	row, err := s.q.CreateAsset(ctx, releasedb.CreateAssetParams{
		ReleaseID:                    releaseID,
		Name:                         name,
		ContentType:                  contentType,
		SizeBytes:                    sizeBytes,
		StorageKey:                   storageKey,
		UploadedActorKind:            string(uploadedActor.Kind),
		UploadedActorUserID:          upUserID,
		UploadedActorRoleKey:         uploadedActor.RoleKey,
		UploadedActorWorkflowRunID:   upWfID,
		UploadedActorDisplayName:     uploadedActor.DisplayName,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrAssetConflict
		}
		return nil, err
	}
	return rowToAsset(row), nil
}

func (s *PostgresAssetStore) GetByID(ctx context.Context, id int64) (*domain.Asset, error) {
	row, err := s.q.GetAssetByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrAssetNotFound
		}
		return nil, err
	}
	return rowToAsset(row), nil
}

func (s *PostgresAssetStore) ListByRelease(ctx context.Context, releaseID int64) ([]*domain.Asset, error) {
	rows, err := s.q.ListAssetsByRelease(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Asset, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToAsset(r))
	}
	return out, nil
}

func (s *PostgresAssetStore) Delete(ctx context.Context, id int64) error {
	n, err := s.q.DeleteAsset(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrAssetNotFound
	}
	return nil
}

// AssetStorage handles on-disk storage of custom release assets.
// Assets are stored under a configurable base directory, keyed by
// release ID + name so they're scoped per release.
type AssetStorage struct {
	baseDir string
}

type AssetStorageDeps struct {
	Config *config.Config
}

func NewAssetStorage(deps *AssetStorageDeps) *AssetStorage {
	return &AssetStorage{baseDir: deps.Config.Storage.AssetsPath}
}

// Store writes the asset body to disk under the given storageKey.
func (s *AssetStorage) Store(storageKey string, body io.Reader) (int64, error) {
	p := filepath.Join(s.baseDir, storageKey)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return 0, fmt.Errorf("create asset dir: %w", err)
	}
	f, err := os.Create(p)
	if err != nil {
		return 0, fmt.Errorf("create asset file: %w", err)
	}
	defer f.Close()
	n, err := io.Copy(f, body)
	if err != nil {
		os.Remove(p)
		return 0, fmt.Errorf("write asset: %w", err)
	}
	return n, nil
}

// Open returns a reader for the stored asset.
func (s *AssetStorage) Open(storageKey string) (io.ReadCloser, error) {
	f, err := os.Open(filepath.Join(s.baseDir, storageKey))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, domain.ErrAssetNotFound
		}
		return nil, err
	}
	return f, nil
}

// Remove deletes the stored asset file.
func (s *AssetStorage) Remove(storageKey string) error {
	err := os.Remove(filepath.Join(s.baseDir, storageKey))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// rowToRelease maps a sqlc Release row to a domain.Release.
func rowToRelease(r releasedb.Release) *domain.Release {
	out := &domain.Release{
		ID:              r.ID,
		RepoID:          r.RepoID,
		TagName:         r.TagName,
		TargetCommitSHA: r.TargetCommitSha,
		Title:           r.Title,
		Notes:           r.Notes,
		IsDraft:         r.IsDraft,
		CreatedActor:    releaseActorRef(r.CreatedActorKind, r.CreatedActorUserID, r.CreatedActorRoleKey, r.CreatedActorWorkflowRunID, r.CreatedActorDisplayName),
		PublishedActor:  releaseActorRef(r.PublishedActorKind, r.PublishedActorUserID, r.PublishedActorRoleKey, r.PublishedActorWorkflowRunID, r.PublishedActorDisplayName),
		CreatedAt: r.CreatedAt.Time,
		UpdatedAt: r.UpdatedAt.Time,
	}
	if r.PublishedAt.Valid {
		out.PublishedAt = r.PublishedAt.Time
	}
	return out
}

// rowToAsset maps a sqlc ReleaseAsset row to a domain.Asset.
func rowToAsset(r releasedb.ReleaseAsset) *domain.Asset {
	return &domain.Asset{
		ID:          r.ID,
		ReleaseID:   r.ReleaseID,
		Name:        r.Name,
		ContentType: r.ContentType,
		SizeBytes:   r.SizeBytes,
		StorageKey:  r.StorageKey,
		UploadedActor: releaseActorRef(r.UploadedActorKind, r.UploadedActorUserID, r.UploadedActorRoleKey, r.UploadedActorWorkflowRunID, r.UploadedActorDisplayName),
		CreatedAt: r.CreatedAt.Time,
	}
}

// releaseActorRef builds an actor.Ref from release/asset actor columns,
// filling the stable business-key ID that the frontend contract expects.
func releaseActorRef(kind string, userID pgtype.Int8, roleKey string, workflowRunID pgtype.Int8, displayName string) actor.Ref {
	k := actor.Kind(kind)
	if k == "" {
		return actor.Ref{}
	}
	return actor.Ref{
		Kind:          k,
		ID:            actorIDForKind(k, userID.Int64, roleKey, workflowRunID.Int64),
		DisplayName:   displayName,
		UserID:        userID.Int64,
		RoleKey:       roleKey,
		WorkflowRunID: workflowRunID.Int64,
	}
}

// actorIDForKind builds the stable ID string from kind-specific identifiers.
func actorIDForKind(k actor.Kind, userID int64, roleKey string, workflowRunID int64) string {
	switch k {
	case actor.KindUser:
		return fmt.Sprintf("user:%d", userID)
	case actor.KindAgent:
		return fmt.Sprintf("agent:%s", roleKey)
	case actor.KindWorkflow:
		return fmt.Sprintf("workflow:run:%d", workflowRunID)
	default:
		return "system:server"
	}
}
