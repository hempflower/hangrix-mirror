// Package service implements the actor Resolver — the runtime-to-actor
// bridge consumed by cross-module callers.
package service

import (
	"context"
	"fmt"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	"github.com/hangrix/hangrix/pkg/actor"
)

// Resolver satisfies domain.Resolver. It wraps domain.Store and
// adds the dispatch logic: From, System, and (in a follow-up)
// FromCommitContext / FromSession.
type Resolver struct {
	store domain.Store
}

// ResolverDeps carries the single dependency.
type ResolverDeps struct {
	Store domain.Store
}

// NewResolver wires the resolver from its deps. The ioc container
// provides Store (the Postgres implementation infra binds to
// domain.Store).
func NewResolver(deps *ResolverDeps) *Resolver {
	return &Resolver{store: deps.Store}
}

// System returns the singleton system actor. The id=1 row is seeded
// by the migration; callers that need the full Ref (with ActorID set)
// should use From(ctx, actor.SystemRef()) instead.
func (r *Resolver) System() actor.Ref {
	return actor.SystemRef()
}

// UserID resolves an actor ID back to its user ID. Returns (0, false)
// when the actor doesn't exist or isn't a 'user' kind actor.
func (r *Resolver) UserID(ctx context.Context, actorID int64) (int64, bool) {
	if actorID <= 0 {
		return 0, false
	}
	ref, err := r.store.GetByID(ctx, actorID)
	if err != nil || ref.Kind != actor.KindUser {
		return 0, false
	}
	return ref.UserID, true
}

// From resolves an actor from a Ref. When ref.ActorID > 0 the row is
// fetched by PK (fast path for already-resolved actors). Otherwise
// the resolver calls the appropriate Ensure* method based on Kind:
//
//	KindUser       → EnsureUser(ref.UserID, ref.DisplayName)
//	KindAgent      → EnsureAgentRole(ref.RoleKey)
//	KindAgentSession → EnsureAgentSession(ref.SessionID, ref.RoleKey)
//	KindWorkflow   → EnsureWorkflowRun(ref.WorkflowRunID, ref.DisplayName)
//	KindBot        → EnsureBot(ref.RoleKey)
//	KindSystem     → System() (returns the seed row)
func (r *Resolver) From(ctx context.Context, ref actor.Ref) (*actor.Ref, error) {
	// Fast path: already resolved.
	if ref.ActorID > 0 {
		a, err := r.store.GetByID(ctx, ref.ActorID)
		if err != nil {
			return nil, fmt.Errorf("actor resolver: get by id %d: %w", ref.ActorID, err)
		}
		return a, nil
	}

	switch ref.Kind {
	case actor.KindUser:
		a, err := r.store.EnsureUser(ctx, ref.UserID, ref.DisplayName)
		if err != nil {
			return nil, fmt.Errorf("actor resolver: ensure user %d: %w", ref.UserID, err)
		}
		return a, nil
	case actor.KindAgent:
		a, err := r.store.EnsureAgentRole(ctx, ref.RoleKey)
		if err != nil {
			return nil, fmt.Errorf("actor resolver: ensure agent_role %s: %w", ref.RoleKey, err)
		}
		return a, nil
	case actor.KindAgentSession:
		a, err := r.store.EnsureAgentSession(ctx, ref.SessionID, ref.RoleKey)
		if err != nil {
			return nil, fmt.Errorf("actor resolver: ensure agent_session %d: %w", ref.SessionID, err)
		}
		return a, nil
	case actor.KindWorkflow:
		a, err := r.store.EnsureWorkflowRun(ctx, ref.WorkflowRunID, ref.DisplayName)
		if err != nil {
			return nil, fmt.Errorf("actor resolver: ensure workflow_run %d: %w", ref.WorkflowRunID, err)
		}
		return a, nil
	case actor.KindBot:
		a, err := r.store.EnsureBot(ctx, ref.RoleKey)
		if err != nil {
			return nil, fmt.Errorf("actor resolver: ensure bot %s: %w", ref.RoleKey, err)
		}
		return a, nil
	case actor.KindSystem, "":
		// System is always id=1; fetch it so the caller gets ActorID set.
		a, err := r.store.GetByID(ctx, 1)
		if err != nil {
			return nil, fmt.Errorf("actor resolver: get system: %w", err)
		}
		return a, nil
	default:
		return nil, fmt.Errorf("actor resolver: unknown kind %q", ref.Kind)
	}
}
