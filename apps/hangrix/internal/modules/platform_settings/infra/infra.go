// Package infra holds the Postgres-backed implementation of the
// platform_settings domain. The SQL surface lives in queries.sql; sqlc
// generates the typed accessors under platformsettingsdb/.
package infra

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings/infra/platformsettingsdb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresRepo satisfies the narrow persistence interface needed by the
// caching service layer. It only does raw reads/writes — validation and
// default fallback live in the service.
type PostgresRepo struct {
	q    *platformsettingsdb.Queries
	pool *pgxpool.Pool
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
		q:    platformsettingsdb.New(deps.Pool),
		pool: deps.Pool,
	}
}

// ListAll returns every persisted setting.
func (r *PostgresRepo) ListAll(ctx context.Context) ([]domain.Setting, error) {
	rows, err := r.q.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Setting, 0, len(rows))
	for _, row := range rows {
		out = append(out, settingFromRow(row))
	}
	return out, nil
}

// GetByKey returns one persisted setting, or pgx.ErrNoRows when absent.
func (r *PostgresRepo) GetByKey(ctx context.Context, key string) (*domain.Setting, error) {
	row, err := r.q.GetByKey(ctx, key)
	if err != nil {
		return nil, err
	}
	s := settingFromRow(row)
	return &s, nil
}

// Upsert inserts or updates a setting row.
func (r *PostgresRepo) Upsert(ctx context.Context, key, value string, updatedBy *int64) error {
	var ub pgtype.Int8
	if updatedBy != nil {
		ub = pgtype.Int8{Int64: *updatedBy, Valid: true}
	}
	return r.q.UpsertSetting(ctx, platformsettingsdb.UpsertSettingParams{
		Key:       key,
		Value:     value,
		UpdatedBy: ub,
	})
}

func settingFromRow(row platformsettingsdb.PlatformSetting) domain.Setting {
	s := domain.Setting{
		Key:       domain.Key(row.Key),
		Value:     row.Value,
		UpdatedAt: row.UpdatedAt.Time,
	}
	if row.UpdatedBy.Valid {
		v := row.UpdatedBy.Int64
		s.UpdatedBy = &v
	}
	return s
}
