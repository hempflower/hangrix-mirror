// Package service implements the actor module's Resolver — the runtime-to-
// actor bridge that translates pkg/actor.Ref values into normalised actor
// IDs via the Store. It is a singleton registered in the ioc container so
// every downstream consumer shares one resolution cache.
//
// §6.1 of the actor-table design doc.
package service

import (
	"context"
	"fmt"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	"github.com/hangrix/hangrix/pkg/actor"
)

// Resolver bridges pkg/actor.Ref values to normalised actor IDs. It wraps
// the Store with resolution logic so callers don't need to know which
// Ensure* method to call for a given Ref.
type Resolver struct {
	store domain.Store
}

// ResolverDeps declares the interfaces Resolver needs from other modules.
type ResolverDeps struct {
	Store domain.Store
}

// NewResolver creates the singleton resolver.
func NewResolver(deps *ResolverDeps) *Resolver {
	return &Resolver{store: deps.Store}
}

// Resolve ensures the given actor exists in the actors table and returns
// its normalised ID. The Ref.Kind drives which Ensure* method is called.
// The returned Ref is a copy with ActorID populated.
func (r *Resolver) Resolve(ctx context.Context, ref actor.Ref) (actor.Ref, error) {
	if ref.IsZero() {
		return actor.Ref{}, fmt.Errorf("resolve: zero actor ref")
	}

	id, err := r.resolveByKind(ctx, ref)
	if err != nil {
		return actor.Ref{}, err
	}

	ref.ActorID = id
	return ref, nil
}

func (r *Resolver) resolveByKind(ctx context.Context, ref actor.Ref) (int64, error) {
	switch ref.Kind {
	case actor.KindUser:
		return r.store.EnsureUser(ctx, ref.UserID, ref.DisplayName)
	case actor.KindAgent:
		return r.store.EnsureAgentRole(ctx, ref.RoleKey)
	case actor.KindAgentSession:
		return r.store.EnsureAgentSession(ctx, ref.AgentSessionID, ref.RoleKey, ref.DisplayName)
	case actor.KindBot:
		return r.store.EnsureBot(ctx, ref.BotID, ref.DisplayName)
	case actor.KindWorkflow:
		return r.store.EnsureWorkflowRun(ctx, ref.WorkflowRunID, ref.DisplayName)
	case actor.KindSystem:
		return r.store.System(ctx)
	default:
		return 0, fmt.Errorf("resolve: unknown actor kind %q", ref.Kind)
	}
}

// System returns the resolved system actor Ref (ActorID=1).
func (r *Resolver) System(ctx context.Context) (actor.Ref, error) {
	id, err := r.store.System(ctx)
	if err != nil {
		return actor.Ref{}, err
	}
	ref := actor.SystemRef()
	ref.ActorID = id
	return ref, nil
}

// EnsureUser is a convenience wrapper for callers that only have a user ID.
func (r *Resolver) EnsureUser(ctx context.Context, userID int64, username string) (actor.Ref, error) {
	return r.Resolve(ctx, actor.UserRef(userID, username))
}

// EnsureAgentRole is a convenience wrapper for callers that only have a role key.
func (r *Resolver) EnsureAgentRole(ctx context.Context, roleKey string) (actor.Ref, error) {
	return r.Resolve(ctx, actor.AgentRef(roleKey))
}
