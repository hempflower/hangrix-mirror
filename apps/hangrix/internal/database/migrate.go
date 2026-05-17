package database

import (
	"context"
	"fmt"
	"io/fs"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Enabled controls whether Migrate actually runs goose up. Set from
// `database.migrate` config at startup, before any module constructs.
// Defaults to true so the local dev path "just works" with a single
// DSN that owns the schema; production deploys that hand schema DDL
// off to a separate migrator role (recommended on PG 15+ where the
// `public` schema is no longer world-writable) flip this to false and
// run `hangrix migrate` (or any out-of-band goose runner) as the
// privileged role instead.
//
// Package-level state is intentional: every module's infra.go calls
// Migrate during ioc construction, and threading the flag through
// each call site would touch 7 files for no behavioural gain. The
// container builds modules sequentially, so there is no concurrency
// concern around the read either.
var Enabled = true

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
//
// When Enabled is false the call short-circuits (no DDL, no version-table
// touch). Callers can still construct repositories afterwards; failing
// queries surface the missing-table error at first use rather than at
// boot, which matches the "schema is someone else's problem" mental
// model that the flag implies.
func Migrate(pool *pgxpool.Pool, migrationsFS fs.FS, tableName, dir string) error {
	if !Enabled {
		log.Printf("database.Migrate: skipped (%s); database.migrate=false", tableName)
		return nil
	}
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
