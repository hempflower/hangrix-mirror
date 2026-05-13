// Package database provides the *pgxpool.Pool that every module's repo layer
// depends on. The pool is created lazily by NewPool from *config.Config and
// pinged on construction so misconfiguration fails fast at startup.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/pkg/ioc"
)

type PoolDeps struct {
	Config *config.Config
}

// NewPool constructs a pgx connection pool from the DSN in config. It pings the
// database before returning so startup fails loudly if Postgres is unreachable.
func NewPool(deps *PoolDeps) *pgxpool.Pool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, deps.Config.Database.DSN)
	if err != nil {
		panic(fmt.Errorf("connect postgres: %w", err))
	}
	if err := pool.Ping(ctx); err != nil {
		panic(fmt.Errorf("ping postgres: %w", err))
	}
	return pool
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewPool).ToSelf()
	return m
}
