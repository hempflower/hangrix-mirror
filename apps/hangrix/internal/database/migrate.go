package database

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Migrate runs every pending up migration in migrationsFS against pool.
//
// Each call MUST be made with a module-specific tableName so different
// modules track their own version history independently (e.g. "goose_user",
// "goose_repo"). The directory dir is the path inside migrationsFS that
// contains the numbered SQL files; use "." when migrationsFS already roots at
// the migrations directory.
//
// goose API is mutated via package-level setters (SetBaseFS / SetTableName /
// SetDialect), so callers MUST serialize calls to Migrate. The ioc container
// invokes module constructors sequentially during build, which is the only
// expected call site — do not invoke from concurrent goroutines.
func Migrate(pool *pgxpool.Pool, migrationsFS fs.FS, tableName, dir string) error {
	goose.SetBaseFS(migrationsFS)
	goose.SetTableName(tableName)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}

	// goose needs a *sql.DB; adapt the pgx pool without taking a separate
	// connection — stdlib.OpenDBFromPool wraps the same pool.
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	if err := goose.UpContext(context.Background(), db, dir); err != nil {
		return fmt.Errorf("goose up (%s): %w", tableName, err)
	}
	return nil
}
