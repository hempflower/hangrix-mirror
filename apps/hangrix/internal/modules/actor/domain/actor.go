// Package domain declares the actor module's types and the Store interface
// consumed by other modules. An Actor is a normalised identity row in the
// actors table — one row per unique (kind, kind-specific key) pair. The
// service.Resolver bridges pkg/actor.Ref values to actor IDs.
package domain

import (
	"context"
	"errors"
	"time"

	"github.com/hangrix/hangrix/pkg/actor"
)

// Actor is one row of the actors table.
type Actor struct {
	ID              int64
	Kind            actor.Kind
	DisplayName     string
	UserID          *int64
	RoleKey         *string
	WorkflowRunID   *int64
	AgentSessionID  *int64
	BotID           *string
	CreatedAt       time.Time
}

// ToRef converts the persisted actor row into a pkg/actor.Ref suitable
// for JSON wire output. The Ref.ActorID is set to this row's ID so
// consumers can round-trip back to the actors table.
func (a *Actor) ToRef() actor.Ref {
	r := actor.Ref{
		ActorID:     a.ID,
		Kind:        a.Kind,
		DisplayName: a.DisplayName,
	}
	switch a.Kind {
	case actor.KindUser:
		if a.UserID != nil {
			r.UserID = *a.UserID
			r.ID = actor.FormatUserID(*a.UserID)
		}
	case actor.KindAgent:
		if a.RoleKey != nil {
			r.RoleKey = *a.RoleKey
			r.ID = actor.FormatAgentID(*a.RoleKey)
		}
	case actor.KindAgentSession:
		if a.AgentSessionID != nil {
			r.AgentSessionID = *a.AgentSessionID
			r.ID = actor.FormatAgentSessionID(*a.AgentSessionID)
		}
		if a.RoleKey != nil {
			r.RoleKey = *a.RoleKey
		}
	case actor.KindBot:
		if a.BotID != nil {
			r.BotID = *a.BotID
			r.ID = actor.FormatBotID(*a.BotID)
		}
	case actor.KindWorkflow:
		if a.WorkflowRunID != nil {
			r.WorkflowRunID = *a.WorkflowRunID
			r.ID = actor.FormatWorkflowRunID(*a.WorkflowRunID)
		}
	case actor.KindSystem:
		r.ID = "system:server"
	}
	return r
}

// ErrActorNotFound is returned by Store lookups when no row matches.
var ErrActorNotFound = errors.New("actor not found")

// Store is the persistence abstraction for the actors table. The Postgres
// implementation lives in the sibling infra/ package. All Ensure* methods
// are idempotent — concurrent calls for the same kind+key pair resolve to
// the same row without errors.
type Store interface {
	// GetByID returns the actor row for the given primary key.
	GetByID(ctx context.Context, id int64) (*Actor, error)

	// GetByRef resolves a pkg/actor.Ref to an actor ID by looking up the
	// kind-specific key. Returns ErrActorNotFound when no matching row
	// exists.
	GetByRef(ctx context.Context, ref actor.Ref) (int64, error)

	// EnsureUser resolves a user to an actor row, creating it if needed.
	// displayName is stored at create time and updated on subsequent calls.
	EnsureUser(ctx context.Context, userID int64, displayName string) (int64, error)

	// EnsureAgentRole resolves a host-yaml role key to an actor row.
	EnsureAgentRole(ctx context.Context, roleKey string) (int64, error)

	// EnsureAgentSession resolves an agent session to an actor row.
	// The roleKey provides context for the display name.
	EnsureAgentSession(ctx context.Context, sessionID int64, roleKey string, displayName string) (int64, error)

	// EnsureWorkflowRun resolves a workflow run to an actor row.
	EnsureWorkflowRun(ctx context.Context, runID int64, displayName string) (int64, error)

	// EnsureBot resolves a bot (identified by a string key) to an actor row.
	EnsureBot(ctx context.Context, botID string, displayName string) (int64, error)

	// System returns the singleton system actor id (always 1).
	System(ctx context.Context) (int64, error)
}
