// ActorRef is the unified actor model used to identify who performed an action.
// It replaces the scattered author_id / agent_role / author_username fields
// with a single typed object. The backend is the source of truth; for backward
// compatibility the frontend should fall back to legacy fields when actor is absent.
//
// Reference: docs/actor-model.md (design document in issue #186 comment #3358)

export type ActorKind = 'user' | 'agent' | 'workflow' | 'system' | 'agent_session' | 'agent_role' | 'workflow_run' | 'bot'

export interface ActorRef {
  kind: ActorKind
  id: string           // stable business key, e.g. "user:12" / "agent:maintainer" / "workflow:run:45" / "system:server"
  display_name: string
  // actor_id is the optional DB-level actor primary key (actors table).
  // When present it is the preferred routing target for /actors/:id.
  actor_id?: number
  // kind-specific optional fields:
  user_id?: number           // kind=user
  role_key?: string          // kind=agent / agent_role / agent_session
  workflow_run_id?: number   // kind=workflow / workflow_run
}
