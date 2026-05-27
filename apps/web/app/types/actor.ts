// ActorRef is the unified actor model used to identify who performed an action.
// It replaces the scattered author_id / agent_role / author_username fields
// with a single typed object. The backend is the source of truth; for backward
// compatibility the frontend should fall back to legacy fields when actor is absent.
//
// Reference: docs/actor-model.md (design document in issue #186 comment #3358)

// ActorKind is the set of actor categories. The backend is the source of truth
// and may emit new kinds before the frontend has explicit handling for them —
// the ActorAvatar component renders any unknown kind with a generic fallback.
//
// *Legacy kinds* ('agent', 'workflow') are retained for backward compatibility
// with existing API responses during the phased backend migration (Phase 3b–3d).
// Once all downstream FK migrations land they will only appear in old cached data.
export type ActorKind =
  | 'user'
  | 'agent'            // legacy: pre-migration agent (generic)
  | 'agent_session'    // a specific agent session (click → /sessions/:id)
  | 'agent_role'       // a role archetype (click → /actors/:id)
  | 'workflow'         // legacy: pre-migration workflow (generic)
  | 'workflow_run'     // a specific workflow run (click → /runs/:id)
  | 'system'
  | 'bot'              // automated bot / integration

export interface ActorRef {
  kind: ActorKind
  id: string           // stable business key, e.g. "user:12" / "agent:maintainer" / "workflow:run:45" / "system:server"
  display_name: string
  // kind-specific optional fields:
  user_id?: number              // kind=user
  role_key?: string             // kind=agent / agent_session / agent_role
  session_id?: number           // kind=agent_session
  actor_id?: number             // new actor table PK (Phase 3b+)
  workflow_run_id?: number      // kind=workflow / workflow_run
}
