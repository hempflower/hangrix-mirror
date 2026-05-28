// Package infra holds the Postgres-backed implementation of the actor
// domain.Store. SQL lives in queries.sql; sqlc generates the typed
// accessors under actordb/.
package infra

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/infra/actordb"
	"github.com/hangrix/hangrix/pkg/actor"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore satisfies domain.Store using sqlc-generated queries.
type PostgresStore struct {
	q    *actordb.Queries
	pool *pgxpool.Pool
}

// PostgresStoreDeps declares the dependencies the ioc container
// satisfies.
type PostgresStoreDeps struct {
	Pool *pgxpool.Pool
}

// NewPostgresStore applies migrations up-front so a schema drift
// surfaces at startup, not on the first Ensure* call.
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

// GetByID fetches an actor by its primary key.
func (s *PostgresStore) GetByID(ctx context.Context, id int64) (*actor.Ref, error) {
	row, err := s.q.GetActorByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get actor by id %d: %w", id, err)
	}
	return rowToRef(row), nil
}

// GetByRef looks up an existing actor by (kind, discriminant).
func (s *PostgresStore) GetByRef(ctx context.Context, ref actor.Ref) (*actor.Ref, error) {
	params := actordb.GetActorByRefParams{
		Kind: string(ref.Kind),
	}
	switch ref.Kind {
	case actor.KindUser:
		params.UserID = pgtype.Int8{Int64: ref.UserID, Valid: ref.UserID > 0}
	case actor.KindAgent, actor.KindBot:
		params.AgentRoleKey = pgtype.Text{String: ref.RoleKey, Valid: ref.RoleKey != ""}
	case actor.KindAgentSession:
		params.AgentSessionID = pgtype.Int8{Int64: ref.SessionID, Valid: ref.SessionID > 0}
	case actor.KindWorkflow:
		params.WorkflowRunID = pgtype.Int8{Int64: ref.WorkflowRunID, Valid: ref.WorkflowRunID > 0}
	default:
		return nil, fmt.Errorf("get actor by ref: unsupported kind %q", ref.Kind)
	}
	row, err := s.q.GetActorByRef(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("get actor by ref %s: %w", ref.ID, err)
	}
	return rowToRef(row), nil
}

// EnsureUser is idempotent: returns the existing or freshly-created user actor.
func (s *PostgresStore) EnsureUser(ctx context.Context, userID int64, displayName string) (*actor.Ref, error) {
	row, err := s.q.EnsureUser(ctx, actordb.EnsureUserParams{
		UserID:      pgtype.Int8{Int64: userID, Valid: true},
		DisplayName: displayName,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure user actor %d: %w", userID, err)
	}
	return rowToRef(row), nil
}

// EnsureAgentRole is idempotent for agent_role actors.
func (s *PostgresStore) EnsureAgentRole(ctx context.Context, roleKey string) (*actor.Ref, error) {
	row, err := s.q.EnsureAgentRole(ctx, actordb.EnsureAgentRoleParams{
		AgentRoleKey: pgtype.Text{String: roleKey, Valid: true},
		DisplayName:  fmt.Sprintf("@agent-%s", roleKey),
	})
	if err != nil {
		return nil, fmt.Errorf("ensure agent_role actor %s: %w", roleKey, err)
	}
	return rowToRef(row), nil
}

// EnsureAgentSession is idempotent for agent_session actors.
func (s *PostgresStore) EnsureAgentSession(ctx context.Context, sessionID int64, roleKey string) (*actor.Ref, error) {
	row, err := s.q.EnsureAgentSession(ctx, actordb.EnsureAgentSessionParams{
		AgentSessionID: pgtype.Int8{Int64: sessionID, Valid: true},
		RoleKey:        pgtype.Text{String: roleKey, Valid: true},
		DisplayName:    fmt.Sprintf("@agent-%s#%d", roleKey, sessionID),
	})
	if err != nil {
		return nil, fmt.Errorf("ensure agent_session actor %d: %w", sessionID, err)
	}
	return rowToRef(row), nil
}

// EnsureWorkflowRun is idempotent for workflow_run actors.
func (s *PostgresStore) EnsureWorkflowRun(ctx context.Context, runID int64, displayName string) (*actor.Ref, error) {
	row, err := s.q.EnsureWorkflowRun(ctx, actordb.EnsureWorkflowRunParams{
		WorkflowRunID: pgtype.Int8{Int64: runID, Valid: true},
		DisplayName:   displayName,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure workflow_run actor %d: %w", runID, err)
	}
	return rowToRef(row), nil
}

// EnsureBot is idempotent for bot actors.
func (s *PostgresStore) EnsureBot(ctx context.Context, name string) (*actor.Ref, error) {
	row, err := s.q.EnsureBot(ctx, actordb.EnsureBotParams{
		Name:        pgtype.Text{String: name, Valid: true},
		DisplayName: name,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure bot actor %s: %w", name, err)
	}
	return rowToRef(row), nil
}

// MigrationsFS exposes the embedded migrations to the database module
// so goose can apply them at startup.
func MigrationsFS() embed.FS { return migrationsFS }

// rowToRef maps a generated actordb.Actor row to the pkg/actor.Ref.
func rowToRef(r actordb.Actor) *actor.Ref {
	ref := &actor.Ref{
		ActorID:     r.ID,
		Kind:        actor.Kind(r.Kind),
		DisplayName: r.DisplayName,
	}
	switch r.Kind {
	case "user":
		if r.UserID.Valid {
			ref.UserID = r.UserID.Int64
			ref.ID = fmt.Sprintf("user:%d", r.UserID.Int64)
		}
	case "agent_role":
		if r.AgentRoleKey.Valid {
			ref.RoleKey = r.AgentRoleKey.String
			ref.ID = fmt.Sprintf("agent:%s", r.AgentRoleKey.String)
		}
	case "agent_session":
		if r.AgentSessionID.Valid {
			ref.SessionID = r.AgentSessionID.Int64
			ref.ID = fmt.Sprintf("agent_session:%d", r.AgentSessionID.Int64)
		}
		if r.AgentRoleKey.Valid {
			ref.RoleKey = r.AgentRoleKey.String
		}
	case "bot":
		if r.AgentRoleKey.Valid {
			ref.RoleKey = r.AgentRoleKey.String
			ref.ID = fmt.Sprintf("bot:%s", r.AgentRoleKey.String)
		}
	case "workflow_run":
		if r.WorkflowRunID.Valid {
			ref.WorkflowRunID = r.WorkflowRunID.Int64
			ref.ID = fmt.Sprintf("workflow:run:%d", r.WorkflowRunID.Int64)
		}
	case "system":
		ref.ID = "system:server"
	}
	return ref
}

// Ensure domain.Store is satisfied.
var _ domain.Store = (*PostgresStore)(nil)
