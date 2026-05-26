package actor

import (
	"context"
	"net/http"
)

type ctxKey int

const workflowActorKey ctxKey = iota

// WithWorkflowActor stores a workflow actor.Ref in the request context so
// downstream handlers (e.g. release write paths) can attribute side effects
// to the correct workflow rather than falling back to SystemRef().
func WithWorkflowActor(r *http.Request, ref Ref) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), workflowActorKey, ref))
}

// WorkflowActorFromRequest returns the workflow actor stored in the request
// context, or the zero value if none was set.
func WorkflowActorFromRequest(r *http.Request) (Ref, bool) {
	ref, ok := r.Context().Value(workflowActorKey).(Ref)
	return ref, ok
}
