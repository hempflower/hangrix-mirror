// Package domain declares the actor module's persistence contract and
// the Resolver interface consumed by cross-module callers.
package domain

import (
	"context"

	"github.com/hangrix/hangrix/pkg/actor"
)

// Store is the persistence abstraction for the actors table. Every
// Ensure* method is idempotent — concurrent callers racing to ensure
// the same (kind, discriminant) tuple all get the same row back
// (partial unique index + ON CONFLICT DO NOTHING ... RETURNING).
type Store interface {
	// GetByID returns the actor row by its primary key.
	GetByID(ctx context.Context, id int64) (*actor.Ref, error)

	// GetByRef looks up an existing actor by (kind, discriminant).
	// Returns nil when no row matches — callers should fall back to
	// the matching Ensure* method.
	GetByRef(ctx context.Context, ref actor.Ref) (*actor.Ref, error)

	// EnsureUser ensures a 'user' actor row exists for the given
	// (user_id, display_name) tuple and returns its Ref.
	EnsureUser(ctx context.Context, userID int64, displayName string) (*actor.Ref, error)

	// EnsureAgentRole ensures an 'agent_role' actor row exists for
	// the given agent_role_key and returns its Ref.
	EnsureAgentRole(ctx context.Context, roleKey string) (*actor.Ref, error)

	// EnsureAgentSession ensures an 'agent_session' actor row exists
	// for the given (agent_session_id, role_key) tuple and returns
	// its Ref.
	EnsureAgentSession(ctx context.Context, sessionID int64, roleKey string) (*actor.Ref, error)

	// EnsureWorkflowRun ensures a 'workflow_run' actor row exists for
	// the given (workflow_run_id, display_name) tuple and returns
	// its Ref.
	EnsureWorkflowRun(ctx context.Context, runID int64, displayName string) (*actor.Ref, error)

	// EnsureBot ensures a 'bot' actor row exists for the given bot
	// name and returns its Ref.
	EnsureBot(ctx context.Context, name string) (*actor.Ref, error)
}

// Resolver is the runtime-to-actor bridge consumed by cross-module
// callers (platform_api, issue handler, spawner). It wraps Store
// with business rules: FromSession, FromCommitContext, System, etc.
type Resolver interface {
	// From resolves an actor from a Ref. If the Ref carries an
	// ActorID > 0 the row is fetched by PK; otherwise the resolver
	// calls the appropriate Ensure* method based on Kind.
	From(ctx context.Context, ref actor.Ref) (*actor.Ref, error)

	// System returns the singleton system actor (id=1).
	System() actor.Ref

	// UserID resolves an actor ID back to its user ID. Returns
	// (0, false) when the actor doesn't exist or isn't a user.
	// Useful for DTOs that still emit a legacy "user_id"-shaped key.
	UserID(ctx context.Context, actorID int64) (int64, bool)
}
