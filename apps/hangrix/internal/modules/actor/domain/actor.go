// Package domain declares the actor model — the unified identity table
// that replaces the scattered author_id/agent_role pattern across the
// platform. Every recorded action references an actors row.
package domain

import (
	"context"
	"errors"
	"time"
)

// Kind enumerates the actor classes stored in actors.kind.
// Mirrors pkg/actor.Kind; kept separate so domain has no pkg dependency.
type Kind string

const (
	KindUser         Kind = "user"
	KindAgentSession Kind = "agent_session"
	KindAgentRole    Kind = "agent_role"
	KindWorkflowRun  Kind = "workflow_run"
	KindBot          Kind = "bot"
	KindSystem       Kind = "system"
)

// Actor is the full row from the actors table.
type Actor struct {
	ID             int64
	Kind           Kind
	DisplayName    string
	UserID         *int64
	AgentSessionID *int64
	WorkflowRunID  *int64
	RepoID         *int64
	RoleKey        string
	BotHandle      string
	CreatedAt      time.Time
}

// Store is the persistence abstraction for the actors table. All Ensure*
// methods are idempotent — they use ON CONFLICT DO NOTHING … RETURNING
// with partial unique indexes per kind. Concurrent callers get the same
// row; no duplicate rows are ever created.
type Store interface {
	// GetByID returns a single actor by PK.
	GetByID(ctx context.Context, id int64) (*Actor, error)

	// GetByRef looks up an actor by its natural key (kind + payload).
	// kind determines which payload field is significant.
	GetByRef(ctx context.Context, kind Kind, userID int64, sessionID int64, workflowRunID int64, repoID int64, roleKey string, botHandle string) (*Actor, error)

	// EnsureUser upserts a kind='user' actor for the given user ID.
	// The display_name is read from the users table on insert.
	EnsureUser(ctx context.Context, userID int64) (*Actor, error)

	// EnsureAgentRole upserts a kind='agent_role' actor for the given
	// (repo_id, role_key) pair. The display_name is derived from the
	// role key.
	EnsureAgentRole(ctx context.Context, repoID int64, roleKey string) (*Actor, error)

	// EnsureAgentSession upserts a kind='agent_session' actor for the
	// given agent_sessions row ID.
	EnsureAgentSession(ctx context.Context, sessionID int64) (*Actor, error)

	// EnsureWorkflowRun upserts a kind='workflow_run' actor for the
	// given workflow_runs row ID.
	EnsureWorkflowRun(ctx context.Context, runID int64) (*Actor, error)

	// EnsureBot upserts a kind='bot' actor for the given handle.
	EnsureBot(ctx context.Context, handle string) (*Actor, error)

	// System returns the singleton system actor (id=1). It never calls
	// Ensure — the row is seeded by the migration.
	System(ctx context.Context) (*Actor, error)
}

// Resolver is the runtime-to-actor bridge consumed by all callers that
// need to convert a calling context (user, agent session, workflow run,
// etc.) into an actors.id. It wraps Store, applying the correct
// Ensure* method for each caller shape.
type Resolver interface {
	// FromUser resolves a user-id actor.
	FromUser(ctx context.Context, userID int64) (*Actor, error)

	// FromAgentRole resolves a persistent agent-role actor.
	FromAgentRole(ctx context.Context, repoID int64, roleKey string) (*Actor, error)

	// FromAgentSession resolves a per-session agent actor.
	FromAgentSession(ctx context.Context, sessionID int64) (*Actor, error)

	// FromWorkflowRun resolves a workflow-run actor.
	FromWorkflowRun(ctx context.Context, runID int64) (*Actor, error)

	// System returns the singleton system actor.
	System(ctx context.Context) (*Actor, error)
}

// Sentinel errors.
var (
	ErrActorNotFound = errors.New("actor not found")
)
