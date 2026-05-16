// Package infra holds the Postgres-backed implementation of the user domain's
// Repo interface. Migrations live in migrations/ and are applied via the
// shared database.Migrate helper at construction time. Only this package may
// import the sqlc-generated userdb subpackage.
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
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/infra/userdb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresRepo implements domain.Repo against a Postgres pool via sqlc-generated
// queries. The struct name encodes the storage backend so a future Sqlite or
// memory impl can sit beside it without ambiguity.
type PostgresRepo struct {
	q *userdb.Queries
}

type PostgresRepoDeps struct {
	Pool *pgxpool.Pool
}

func NewPostgresRepo(deps *PostgresRepoDeps) *PostgresRepo {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("user migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_user", "."); err != nil {
		panic(fmt.Errorf("apply user migrations: %w", err))
	}
	return &PostgresRepo{q: userdb.New(deps.Pool)}
}

func (r *PostgresRepo) Create(ctx context.Context, username, email, passwordHash string, role domain.Role) (*domain.User, error) {
	row, err := r.q.CreateUser(ctx, userdb.CreateUserParams{
		Username:     username,
		Email:        email,
		PasswordHash: passwordHash,
		Role:         string(role),
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrUserConflict
		}
		return nil, err
	}
	return rowToUser(row), nil
}

func (r *PostgresRepo) GetByID(ctx context.Context, id int64) (*domain.User, error) {
	row, err := r.q.GetUserByID(ctx, id)
	return mapLookup(row, err)
}

func (r *PostgresRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	row, err := r.q.GetUserByUsername(ctx, username)
	return mapLookup(row, err)
}

func (r *PostgresRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	row, err := r.q.GetUserByEmail(ctx, email)
	return mapLookup(row, err)
}

func (r *PostgresRepo) Count(ctx context.Context) (int64, error) {
	return r.q.CountUsers(ctx)
}

func (r *PostgresRepo) List(ctx context.Context, offset, limit int32) ([]*domain.User, error) {
	rows, err := r.q.ListUsers(ctx, userdb.ListUsersParams{Limit: limit, Offset: offset})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.User, 0, len(rows))
	for i := range rows {
		out = append(out, rowToUser(rows[i]))
	}
	return out, nil
}

func (r *PostgresRepo) UpdateProfile(ctx context.Context, id int64, email string) (*domain.User, error) {
	row, err := r.q.UpdateUserProfile(ctx, userdb.UpdateUserProfileParams{ID: id, Email: email})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrUserConflict
		}
		return mapLookup(row, err)
	}
	return rowToUser(row), nil
}

func (r *PostgresRepo) UpdatePassword(ctx context.Context, id int64, passwordHash string) error {
	return r.q.UpdateUserPassword(ctx, userdb.UpdateUserPasswordParams{ID: id, PasswordHash: passwordHash})
}

func (r *PostgresRepo) UpdateRole(ctx context.Context, id int64, role domain.Role) (*domain.User, error) {
	row, err := r.q.UpdateUserRole(ctx, userdb.UpdateUserRoleParams{ID: id, Role: string(role)})
	return mapLookup(row, err)
}

func (r *PostgresRepo) UpdateDisabled(ctx context.Context, id int64, disabled bool) (*domain.User, error) {
	row, err := r.q.UpdateUserDisabled(ctx, userdb.UpdateUserDisabledParams{ID: id, Disabled: disabled})
	return mapLookup(row, err)
}

func mapLookup(row userdb.User, err error) (*domain.User, error) {
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrUserNotFound
		}
		return nil, err
	}
	return rowToUser(row), nil
}

func rowToUser(r userdb.User) *domain.User {
	return &domain.User{
		ID:           r.ID,
		Username:     r.Username,
		Email:        r.Email,
		PasswordHash: r.PasswordHash,
		Role:         domain.Role(r.Role),
		Disabled:     r.Disabled,
		CreatedAt:    r.CreatedAt.Time,
		UpdatedAt:    r.UpdatedAt.Time,
	}
}
