package infra

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra/repodb"
)

// PostgresProtectionStore implements domain.ProtectionStore on top of the
// shared pgx pool. It lives in its own struct (rather than as more methods on
// PostgresStore) so the repo-CRUD and protection-CRUD method names — Create /
// Delete in particular — don't collide on the same receiver. The two stores
// happen to share a DB but otherwise stand alone; the ioc container wires
// each interface independently.
type PostgresProtectionStore struct {
	q *repodb.Queries
}

type PostgresProtectionStoreDeps struct {
	Pool *pgxpool.Pool
}

// NewPostgresProtectionStore relies on NewPostgresStore having already run
// the repo migrations (00002_create_branch_protections.sql lives there) —
// ioc's per-module sequential build orders the two constructors, but they
// share the migration table.
func NewPostgresProtectionStore(deps *PostgresProtectionStoreDeps) *PostgresProtectionStore {
	return &PostgresProtectionStore{q: repodb.New(deps.Pool)}
}

func (s *PostgresProtectionStore) List(ctx context.Context, repoID int64) ([]*domain.BranchProtection, error) {
	rows, err := s.q.ListBranchProtectionsByRepo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.BranchProtection, 0, len(rows))
	for _, r := range rows {
		out = append(out, protectionRowToDomain(r))
	}
	return out, nil
}

func (s *PostgresProtectionStore) Get(ctx context.Context, id, repoID int64) (*domain.BranchProtection, error) {
	row, err := s.q.GetBranchProtection(ctx, repodb.GetBranchProtectionParams{ID: id, RepoID: repoID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrProtectionNotFound
		}
		return nil, err
	}
	return protectionRowToDomain(row), nil
}

func (s *PostgresProtectionStore) Create(ctx context.Context, repoID int64, pattern string, forbidForcePush, forbidDelete, forbidDirectPush bool) (*domain.BranchProtection, error) {
	row, err := s.q.CreateBranchProtection(ctx, repodb.CreateBranchProtectionParams{
		RepoID:           repoID,
		Pattern:          pattern,
		ForbidForcePush:  forbidForcePush,
		ForbidDelete:     forbidDelete,
		ForbidDirectPush: forbidDirectPush,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrProtectionConflict
		}
		return nil, err
	}
	return protectionRowToDomain(row), nil
}

func (s *PostgresProtectionStore) Update(ctx context.Context, id, repoID int64, pattern string, forbidForcePush, forbidDelete, forbidDirectPush bool) (*domain.BranchProtection, error) {
	row, err := s.q.UpdateBranchProtection(ctx, repodb.UpdateBranchProtectionParams{
		ID:               id,
		RepoID:           repoID,
		Pattern:          pattern,
		ForbidForcePush:  forbidForcePush,
		ForbidDelete:     forbidDelete,
		ForbidDirectPush: forbidDirectPush,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrProtectionNotFound
		}
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrProtectionConflict
		}
		return nil, err
	}
	return protectionRowToDomain(row), nil
}

func (s *PostgresProtectionStore) Delete(ctx context.Context, id, repoID int64) error {
	n, err := s.q.DeleteBranchProtection(ctx, repodb.DeleteBranchProtectionParams{ID: id, RepoID: repoID})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrProtectionNotFound
	}
	return nil
}

func protectionRowToDomain(r repodb.BranchProtection) *domain.BranchProtection {
	return &domain.BranchProtection{
		ID:               r.ID,
		RepoID:           r.RepoID,
		Pattern:          r.Pattern,
		ForbidForcePush:  r.ForbidForcePush,
		ForbidDelete:     r.ForbidDelete,
		ForbidDirectPush: r.ForbidDirectPush,
		CreatedAt:        r.CreatedAt.Time,
		UpdatedAt:        r.UpdatedAt.Time,
	}
}
