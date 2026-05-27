// Package infra holds the Postgres-backed implementation of the actor
// domain. SQL lives in queries.sql; sqlc generates the typed accessors
// under actordb/. This file owns row → domain mapping and migration
// bootstrapping.
package infra

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/infra/actordb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore implements domain.Store backed by sqlc-generated queries.
type PostgresStore struct {
	q    *actordb.Queries
	pool *pgxpool.Pool
}

type PostgresStoreDeps struct {
	Pool *pgxpool.Pool
}

func NewPostgresStore(deps *PostgresStoreDeps) *PostgresStore {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("actor migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_actor", "."); err != nil {
		panic(fmt.Errorf("apply actor migrations: %w", err))
	}
	return &PostgresStore{
		q:    actordb.New(deps.Pool),
		pool: deps.Pool,
	}
}

// GetByID returns a single actor by PK.
func (s *PostgresStore) GetByID(ctx context.Context, id int64) (*domain.Actor, error) {
	row, err := s.q.GetActorByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return toDomain(row), nil
}

// GetByRef looks up an actor by its natural key.
func (s *PostgresStore) GetByRef(ctx context.Context, kind domain.Kind, userID int64, sessionID int64, workflowRunID int64, repoID int64, roleKey string, botHandle string) (*domain.Actor, error) {
	row, err := s.q.GetActorByRef(ctx, actordb.GetActorByRefParams{
		Kind:           string(kind),
		UserID:         toPgInt8(userID),
		AgentSessionID: toPgInt8(sessionID),
		WorkflowRunID:  toPgInt8(workflowRunID),
		RepoID:         toPgInt8(repoID),
		RoleKey:        roleKey,
		BotHandle:      botHandle,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return toDomain(row), nil
}

// EnsureUser upserts a kind='user' actor.
func (s *PostgresStore) EnsureUser(ctx context.Context, userID int64) (*domain.Actor, error) {
	row, err := s.q.EnsureUser(ctx, toPgInt8(userID))
	if err != nil {
		return nil, mapErr(err)
	}
	return toDomain(row), nil
}

// EnsureAgentRole upserts a kind='agent_role' actor.
func (s *PostgresStore) EnsureAgentRole(ctx context.Context, repoID int64, roleKey string) (*domain.Actor, error) {
	row, err := s.q.EnsureAgentRole(ctx, actordb.EnsureAgentRoleParams{
		RepoID:  toPgInt8(repoID),
		RoleKey: roleKey,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return toDomain(row), nil
}

// EnsureAgentSession upserts a kind='agent_session' actor.
func (s *PostgresStore) EnsureAgentSession(ctx context.Context, sessionID int64) (*domain.Actor, error) {
	row, err := s.q.EnsureAgentSession(ctx, toPgInt8(sessionID))
	if err != nil {
		return nil, mapErr(err)
	}
	return toDomain(row), nil
}

// EnsureWorkflowRun upserts a kind='workflow_run' actor.
func (s *PostgresStore) EnsureWorkflowRun(ctx context.Context, runID int64) (*domain.Actor, error) {
	row, err := s.q.EnsureWorkflowRun(ctx, toPgInt8(runID))
	if err != nil {
		return nil, mapErr(err)
	}
	return toDomain(row), nil
}

// EnsureBot upserts a kind='bot' actor.
func (s *PostgresStore) EnsureBot(ctx context.Context, handle string) (*domain.Actor, error) {
	row, err := s.q.EnsureBot(ctx, handle)
	if err != nil {
		return nil, mapErr(err)
	}
	return toDomain(row), nil
}

// System returns the singleton system actor (id=1).
func (s *PostgresStore) System(ctx context.Context) (*domain.Actor, error) {
	row, err := s.q.SystemActor(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return toDomain(row), nil
}

// --- helpers ---

func toDomain(row actordb.Actor) *domain.Actor {
	return &domain.Actor{
		ID:             row.ID,
		Kind:           domain.Kind(row.Kind),
		DisplayName:    row.DisplayName,
		UserID:         fromPgInt8(row.UserID),
		AgentSessionID: fromPgInt8(row.AgentSessionID),
		WorkflowRunID:  fromPgInt8(row.WorkflowRunID),
		RepoID:         fromPgInt8(row.RepoID),
		RoleKey:        row.RoleKey,
		BotHandle:      row.BotHandle,
		CreatedAt:      row.CreatedAt.Time,
	}
}

func toPgInt8(v int64) pgtype.Int8 {
	if v == 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: v, Valid: true}
}

func fromPgInt8(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	c := v.Int64
	return &c
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	// pgx wraps "no rows" differently; sqlc surfaces it as sql.ErrNoRows.
	// Map it to a domain sentinel so callers can branch without importing
	// database/sql.
	if err.Error() == "no rows in result set" {
		return domain.ErrActorNotFound
	}
	return err
}
