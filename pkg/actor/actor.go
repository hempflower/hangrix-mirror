// Package actor defines the unified ActorRef / ActorSnapshot value objects
// that replace the scattered author_id/author_name/agent_role pattern
// across the platform. Every recorded action (issue comment, event,
// contribution, release, workflow run) carries an actor.
//
// Persistence strategy (per design doc):
//
//	actor_kind       TEXT   -- 'user' | 'agent' | 'workflow' | 'system'
//	actor_user_id     BIGINT -- NULLable FK to users
//	actor_role_key    TEXT   -- host yaml role key for 'agent' kind
//	actor_workflow_run_id BIGINT -- FK to workflow_runs for 'workflow' kind
//	actor_display_name TEXT  -- snapshot at write time
//
// Read path: new columns first; fall back to legacy author_id/agent_role.
// Write path: always dual-write both new and old columns.
package actor

import (
	"encoding/json"
	"fmt"
)

// Kind enumerates the actor classes.
type Kind string

const (
	KindUser          Kind = "user"
	KindAgent         Kind = "agent"          // agent_role identity — the role-level actor
	KindAgentSession  Kind = "agent_session"  // agent_session identity — the per-session actor
	KindBot           Kind = "bot"            // automated bot identity
	KindWorkflow      Kind = "workflow"
	KindSystem        Kind = "system"
)

// Ref is the unified actor reference carried by every domain object.
// It is both the stable business key (Kind + identifiers) and the
// display-facing snapshot (DisplayName).
type Ref struct {
	Kind           Kind   `json:"kind"`
	ID             string `json:"id"`         // stable key, e.g. "user:12", "agent:maintainer", "workflow:run:45", "system:server"
	DisplayName    string `json:"display_name"`
	ActorID        int64  `json:"actor_id,omitempty"` // PK in the actors table; resolved by the actor module
	UserID         int64  `json:"user_id,omitempty"`
	RoleKey        string `json:"role_key,omitempty"`
	WorkflowRunID  int64  `json:"workflow_run_id,omitempty"`
	SessionID      int64  `json:"session_id,omitempty"` // for KindAgentSession
}

// UserRef builds an actor from a platform user row.
func UserRef(userID int64, username string) Ref {
	return Ref{
		Kind:        KindUser,
		ID:          fmt.Sprintf("user:%d", userID),
		DisplayName: username,
		UserID:      userID,
	}
}

// AgentRef builds an actor from a host yaml role key.
func AgentRef(roleKey string) Ref {
	return Ref{
		Kind:        KindAgent,
		ID:          fmt.Sprintf("agent:%s", roleKey),
		DisplayName: fmt.Sprintf("@agent-%s", roleKey),
		RoleKey:     roleKey,
	}
}

// WorkflowRef builds an actor representing a workflow run.
// workflowName and runID together form the stable identity.
func WorkflowRef(runID int64, workflowName string) Ref {
	id := fmt.Sprintf("workflow:run:%d", runID)
	display := fmt.Sprintf("workflow/%s#%d", workflowName, runID)
	return Ref{
		Kind:          KindWorkflow,
		ID:            id,
		DisplayName:   display,
		WorkflowRunID: runID,
	}
}

// SystemRef builds the singleton system actor.
func SystemRef() Ref {
	return Ref{
		Kind:        KindSystem,
		ID:          "system:server",
		DisplayName: "System",
	}
}

// AgentSessionRef builds an actor from a specific agent session row.
func AgentSessionRef(sessionID int64, roleKey string) Ref {
	return Ref{
		Kind:        KindAgentSession,
		ID:          fmt.Sprintf("agent_session:%d", sessionID),
		DisplayName: fmt.Sprintf("@agent-%s#%d", roleKey, sessionID),
		SessionID:   sessionID,
		RoleKey:     roleKey,
	}
}

// BotRef builds an actor from an automated bot name.
func BotRef(name string) Ref {
	return Ref{
		Kind:        KindBot,
		ID:          fmt.Sprintf("bot:%s", name),
		DisplayName: name,
	}
}


// RefFromColumns reconstructs a Ref from the flat DB columns written by
// the dual-write strategy. Callers should only pass this when kind is
// non-empty; the zero-Ref case is handled by callers checking kind first.
func RefFromColumns(kind Kind, userID int64, roleKey string, workflowRunID int64, displayName string) Ref {
	switch kind {
	case KindUser:
		return Ref{
			Kind:        KindUser,
			ID:          fmt.Sprintf("user:%d", userID),
			DisplayName: displayName,
			UserID:      userID,
		}
	case KindAgent:
		return Ref{
			Kind:        KindAgent,
			ID:          fmt.Sprintf("agent:%s", roleKey),
			DisplayName: displayName,
			RoleKey:     roleKey,
		}
	case KindAgentSession:
		return Ref{
			Kind:        KindAgentSession,
			ID:          fmt.Sprintf("agent_session:%d", workflowRunID), // workflowRunID reused for sessionID in flat-column convention
			DisplayName: displayName,
			SessionID:   workflowRunID,
			RoleKey:     roleKey,
		}
	case KindBot:
		return Ref{
			Kind:        KindBot,
			ID:          fmt.Sprintf("bot:%s", roleKey),
			DisplayName: displayName,
		}
	case KindWorkflow:
		return Ref{
			Kind:          KindWorkflow,
			ID:            fmt.Sprintf("workflow:run:%d", workflowRunID),
			DisplayName:   displayName,
			WorkflowRunID: workflowRunID,
		}
	case KindSystem:
		return SystemRef()
	default:
		return Ref{}
	}
}

// IsZero reports whether r is the zero value (no actor set).
func (r Ref) IsZero() bool { return r.Kind == "" }

// MarshalJSON ensures we never emit a partial actor.
func (r Ref) MarshalJSON() ([]byte, error) {
	if r.IsZero() {
		return []byte("null"), nil
	}
	// Use an alias to avoid infinite recursion.
	type alias Ref
	return json.Marshal(alias(r))
}
