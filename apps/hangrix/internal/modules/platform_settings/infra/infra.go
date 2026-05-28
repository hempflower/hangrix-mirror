// Package infra holds the Postgres-backed implementation of the
// platform_settings domain.Store. SQL lives in queries.sql; sqlc
// generates the typed accessors under platformsettingsdb/.
package infra

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/infra/platformsettingsdb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresRepo implements the narrow `repo` interface the service layer
// consumes — just Get, Upsert, List.
type PostgresRepo struct {
	q *platformsettingsdb.Queries
}

type PostgresRepoDeps struct {
	Pool *pgxpool.Pool
}

func NewPostgresRepo(deps *PostgresRepoDeps) *PostgresRepo {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("platform_settings migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_platform_settings", "."); err != nil {
		panic(fmt.Errorf("apply platform_settings migrations: %w", err))
	}
	return &PostgresRepo{
		q: platformsettingsdb.New(deps.Pool),
	}
}

// GetSetting returns the row for key, or errNotFound.
func (r *PostgresRepo) GetSetting(ctx context.Context, key string) (domain.Setting, error) {
	row, err := r.q.GetSetting(ctx, key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
			return domain.Setting{}, domain.ErrSettingNotFound
		}
		return domain.Setting{}, err
	}
	return domain.Setting{
		Key:         row.Key,
		Value:       row.Value,
		Description: row.Description,
		UpdatedAt:   row.UpdatedAt.Time,
	}, nil
}

// UpsertSetting inserts or updates a setting row.
func (r *PostgresRepo) UpsertSetting(ctx context.Context, key, value, description string) error {
	return r.q.UpsertSetting(ctx, platformsettingsdb.UpsertSettingParams{
		Key:         key,
		Value:       value,
		Description: description,
	})
}

// ListSettings returns every row ordered by key.
func (r *PostgresRepo) ListSettings(ctx context.Context) ([]domain.Setting, error) {
	rows, err := r.q.ListSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Setting, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.Setting{
			Key:         row.Key,
			Value:       row.Value,
			Description: row.Description,
			UpdatedAt:   row.UpdatedAt.Time,
		})
	}
	return out, nil
}

var _ interface {
	GetSetting(context.Context, string) (domain.Setting, error)
} = (*PostgresRepo)(nil)
