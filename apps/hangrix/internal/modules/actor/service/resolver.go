// Package service implements the actor.Resolver runtime-to-actor bridge.
// All callers that need to convert a calling context (user, agent session,
// workflow run, etc.) into an actors.id go through the Resolver.
package service

import (
	"context"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
)

// Resolver wraps the actor Store and applies the correct Ensure* method
// for each caller shape. It is a thin facade — all idempotency and
// concurrency safety lives in the Store's ON CONFLICT DO NOTHING queries.
type Resolver struct {
	store domain.Store
}

type ResolverDeps struct {
	Store domain.Store
}

func NewResolver(deps *ResolverDeps) *Resolver {
	return &Resolver{store: deps.Store}
}

// FromUser resolves a user-id actor.
func (r *Resolver) FromUser(ctx context.Context, userID int64) (*domain.Actor, error) {
	return r.store.EnsureUser(ctx, userID)
}

// FromAgentRole resolves a persistent agent-role actor.
func (r *Resolver) FromAgentRole(ctx context.Context, repoID int64, roleKey string) (*domain.Actor, error) {
	return r.store.EnsureAgentRole(ctx, repoID, roleKey)
}

// FromAgentSession resolves a per-session agent actor.
func (r *Resolver) FromAgentSession(ctx context.Context, sessionID int64) (*domain.Actor, error) {
	return r.store.EnsureAgentSession(ctx, sessionID)
}

// FromWorkflowRun resolves a workflow-run actor.
func (r *Resolver) FromWorkflowRun(ctx context.Context, runID int64) (*domain.Actor, error) {
	return r.store.EnsureWorkflowRun(ctx, runID)
}

// System returns the singleton system actor (id=1).
func (r *Resolver) System(ctx context.Context) (*domain.Actor, error) {
	return r.store.System(ctx)
}
