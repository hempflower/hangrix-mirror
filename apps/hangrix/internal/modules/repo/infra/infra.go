// Package infra holds the Postgres-backed implementation of the repo domain's
// Store interface plus filesystem helpers (Storage) that resolve bare-repo
// paths and delegate creation/deletion to the git module. Migrations live in
// migrations/ and are applied via the shared database.Migrate helper at
// construction time. Only this package may import the sqlc-generated repodb
// subpackage.
package infra

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra/repodb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore implements domain.Store on top of a pgx pool via the
// sqlc-generated repodb queries. The struct name encodes the storage backend
// so a future Sqlite or memory impl can sit beside it without ambiguity.
type PostgresStore struct {
	q *repodb.Queries
}

type PostgresStoreDeps struct {
	Pool *pgxpool.Pool
}

func NewPostgresStore(deps *PostgresStoreDeps) *PostgresStore {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("repo migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_repo", "."); err != nil {
		panic(fmt.Errorf("apply repo migrations: %w", err))
	}
	return &PostgresStore{q: repodb.New(deps.Pool)}
}

func (s *PostgresStore) Create(ctx context.Context, ownerID int64, name, description, defaultBranch string, visibility domain.Visibility) (*domain.Repo, error) {
	row, err := s.q.CreateRepo(ctx, repodb.CreateRepoParams{
		OwnerID:       ownerID,
		Name:          name,
		Description:   description,
		Visibility:    string(visibility),
		DefaultBranch: defaultBranch,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrRepoConflict
		}
		return nil, err
	}
	// Create returns the base row without owner_username; fetch the joined
	// view so the caller gets a fully-populated Repo. This is one extra
	// round-trip per create, which is acceptable given how infrequent
	// creates are.
	full, err := s.q.GetRepoByID(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return joinedRowToRepo(full), nil
}

func (s *PostgresStore) GetByID(ctx context.Context, id int64) (*domain.Repo, error) {
	row, err := s.q.GetRepoByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRepoNotFound
		}
		return nil, err
	}
	return joinedRowToRepo(row), nil
}

func (s *PostgresStore) GetByOwnerAndName(ctx context.Context, ownerID int64, name string) (*domain.Repo, error) {
	row, err := s.q.GetRepoByOwnerAndName(ctx, repodb.GetRepoByOwnerAndNameParams{
		OwnerID: ownerID,
		Name:    name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRepoNotFound
		}
		return nil, err
	}
	return joinedRowToRepo(repodb.GetRepoByIDRow(row)), nil
}

func (s *PostgresStore) ListByOwner(ctx context.Context, ownerID int64, includePrivate bool, offset, limit int32) ([]*domain.Repo, int64, error) {
	rows, err := s.q.ListReposByOwner(ctx, repodb.ListReposByOwnerParams{
		OwnerID:        ownerID,
		Limit:          limit,
		Offset:         offset,
		IncludePrivate: includePrivate,
	})
	if err != nil {
		return nil, 0, err
	}
	total, err := s.q.CountReposByOwner(ctx, repodb.CountReposByOwnerParams{
		OwnerID:        ownerID,
		IncludePrivate: includePrivate,
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]*domain.Repo, 0, len(rows))
	for _, r := range rows {
		out = append(out, joinedRowToRepo(repodb.GetRepoByIDRow(r)))
	}
	return out, total, nil
}

func (s *PostgresStore) Delete(ctx context.Context, id int64) error {
	n, err := s.q.DeleteRepo(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrRepoNotFound
	}
	return nil
}

func (s *PostgresStore) UpdateMeta(ctx context.Context, id int64, description, defaultBranch string, visibility domain.Visibility) (*domain.Repo, error) {
	_, err := s.q.UpdateRepoMeta(ctx, repodb.UpdateRepoMetaParams{
		ID:            id,
		Description:   description,
		DefaultBranch: defaultBranch,
		Visibility:    string(visibility),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRepoNotFound
		}
		return nil, err
	}
	full, err := s.q.GetRepoByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRepoNotFound
		}
		return nil, err
	}
	return joinedRowToRepo(full), nil
}

func joinedRowToRepo(r repodb.GetRepoByIDRow) *domain.Repo {
	return &domain.Repo{
		ID:            r.ID,
		OwnerID:       r.OwnerID,
		OwnerUsername: r.OwnerUsername,
		Name:          r.Name,
		Description:   r.Description,
		Visibility:    domain.Visibility(r.Visibility),
		DefaultBranch: r.DefaultBranch,
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation
}
