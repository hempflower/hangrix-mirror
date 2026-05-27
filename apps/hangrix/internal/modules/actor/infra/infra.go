// Package infra holds the Postgres-backed implementation of the actor
// domain. The SQL surface lives in queries.sql; sqlc generates the typed
// accessors under actordb/. This file owns the (de)serialisation between
// generated row types and the domain model, plus the two-step Ensure*
// pattern (INSERT ON CONFLICT DO NOTHING → SELECT fallback).
package infra

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/infra/actordb"
	"github.com/hangrix/hangrix/pkg/actor"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type PostgresRepo struct {
	q    *actordb.Queries
	pool *pgxpool.Pool
}

// PostgresRepoDeps lists the modules whose migrations must run before
// ours. The actors table FKs users(id), workflow_runs(id), and
// agent_sessions(id) — the dependency chain ensures those tables exist
// before 00001_create_actors.sql runs.
type PostgresRepoDeps struct {
	Pool *pgxpool.Pool
}

// NewPostgresRepo applies migrations up-front so a schema drift surfaces
// at startup, not on the first actor resolution.
func NewPostgresRepo(deps *PostgresRepoDeps) *PostgresRepo {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("actor migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_actor", "."); err != nil {
		panic(fmt.Errorf("apply actor migrations: %w", err))
	}
	return &PostgresRepo{
		q:    actordb.New(deps.Pool),
		pool: deps.Pool,
	}
}

// ---- pgtype helpers ----

func int8Ptr(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func textPtr(v *string) pgtype.Text {
	if v == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *v, Valid: true}
}

func int8FromVal(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: true}
}

func textFromVal(v string) pgtype.Text {
	return pgtype.Text{String: v, Valid: true}
}

// ---- Store implementation ----

// GetByID returns the actor row for the given primary key.
func (r *PostgresRepo) GetByID(ctx context.Context, id int64) (*domain.Actor, error) {
	row, err := r.q.GetActorByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrActorNotFound
		}
		return nil, fmt.Errorf("get actor by id: %w", err)
	}
	return toDomain(row), nil
}

// GetByRef resolves a pkg/actor.Ref to an actor ID by looking up the kind-specific key.
func (r *PostgresRepo) GetByRef(ctx context.Context, ref actor.Ref) (int64, error) {
	switch ref.Kind {
	case actor.KindUser:
		return r.q.GetActorByUserID(ctx, int8FromVal(ref.UserID))
	case actor.KindAgent:
		return r.q.GetActorByRoleKey(ctx, textFromVal(ref.RoleKey))
	case actor.KindAgentSession:
		return r.q.GetActorByAgentSessionID(ctx, int8FromVal(ref.AgentSessionID))
	case actor.KindBot:
		return r.q.GetActorByBotID(ctx, textFromVal(ref.BotID))
	case actor.KindWorkflow:
		return r.q.GetActorByWorkflowRunID(ctx, int8FromVal(ref.WorkflowRunID))
	case actor.KindSystem:
		return r.q.GetSystemActorID(ctx)
	default:
		return 0, domain.ErrActorNotFound
	}
}

// EnsureUser resolves a user to an actor row, creating it if needed.
func (r *PostgresRepo) EnsureUser(ctx context.Context, userID int64, displayName string) (int64, error) {
	id, err := r.q.InsertActor(ctx, actordb.InsertActorParams{
		Kind:        string(actor.KindUser),
		DisplayName: displayName,
		UserID:      int8FromVal(userID),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("ensure user: %w", err)
		}
		// Conflict — row already exists; look it up.
		return r.q.GetActorByUserID(ctx, int8FromVal(userID))
	}
	return id, nil
}

// EnsureAgentRole resolves a host-yaml role key to an actor row.
func (r *PostgresRepo) EnsureAgentRole(ctx context.Context, roleKey string) (int64, error) {
	dn := fmt.Sprintf("@agent-%s", roleKey)
	id, err := r.q.InsertActor(ctx, actordb.InsertActorParams{
		Kind:        string(actor.KindAgent),
		DisplayName: dn,
		RoleKey:     textFromVal(roleKey),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("ensure agent role: %w", err)
		}
		return r.q.GetActorByRoleKey(ctx, textFromVal(roleKey))
	}
	return id, nil
}

// EnsureAgentSession resolves an agent session to an actor row.
func (r *PostgresRepo) EnsureAgentSession(ctx context.Context, sessionID int64, roleKey string, displayName string) (int64, error) {
	rk := roleKey
	id, err := r.q.InsertActor(ctx, actordb.InsertActorParams{
		Kind:           string(actor.KindAgentSession),
		DisplayName:    displayName,
		AgentSessionID: int8FromVal(sessionID),
		RoleKey:        textFromVal(rk),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("ensure agent session: %w", err)
		}
		return r.q.GetActorByAgentSessionID(ctx, int8FromVal(sessionID))
	}
	return id, nil
}

// EnsureWorkflowRun resolves a workflow run to an actor row.
func (r *PostgresRepo) EnsureWorkflowRun(ctx context.Context, runID int64, displayName string) (int64, error) {
	id, err := r.q.InsertActor(ctx, actordb.InsertActorParams{
		Kind:          string(actor.KindWorkflow),
		DisplayName:   displayName,
		WorkflowRunID: int8FromVal(runID),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("ensure workflow run: %w", err)
		}
		return r.q.GetActorByWorkflowRunID(ctx, int8FromVal(runID))
	}
	return id, nil
}

// EnsureBot resolves a bot to an actor row.
func (r *PostgresRepo) EnsureBot(ctx context.Context, botID string, displayName string) (int64, error) {
	bid := botID
	id, err := r.q.InsertActor(ctx, actordb.InsertActorParams{
		Kind:        string(actor.KindBot),
		DisplayName: displayName,
		BotID:       textFromVal(bid),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("ensure bot: %w", err)
		}
		return r.q.GetActorByBotID(ctx, textFromVal(bid))
	}
	return id, nil
}

// System returns the singleton system actor id (always 1).
func (r *PostgresRepo) System(ctx context.Context) (int64, error) {
	return r.q.GetSystemActorID(ctx)
}

// ---- serialisation ----

func toDomain(row actordb.Actor) *domain.Actor {
	a := &domain.Actor{
		ID:          row.ID,
		Kind:        actor.Kind(row.Kind),
		DisplayName: row.DisplayName,
		CreatedAt:   row.CreatedAt.Time,
	}
	if row.UserID.Valid {
		a.UserID = &row.UserID.Int64
	}
	if row.RoleKey.Valid {
		a.RoleKey = &row.RoleKey.String
	}
	if row.WorkflowRunID.Valid {
		a.WorkflowRunID = &row.WorkflowRunID.Int64
	}
	if row.AgentSessionID.Valid {
		a.AgentSessionID = &row.AgentSessionID.Int64
	}
	if row.BotID.Valid {
		a.BotID = &row.BotID.String
	}
	return a
}
