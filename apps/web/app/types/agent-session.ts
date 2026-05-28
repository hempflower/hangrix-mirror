export type ContainerState = 'running' | 'stopped' | 'pending_stop' | 'pending_removal' | 'none'

export interface AdminAgentSession {
  session_id: number
  runner_id?: number
  repo_id: number
  issue_number: number
  role_key: string
  status: string
  repo_sha: string
  cause_kind: string
  cause_id: string
  role_config: unknown
  exit_code?: number
  error_message?: string
  created_at: string
  ended_at?: string | null
  /** Raw server fields — use deriveContainerState() to get the computed state. */
  container_id?: string
  container_last_used_at?: string | null
  container_stopped_at?: string | null
  container_stop_pending?: boolean
  container_cleanup_pending?: boolean
  running_jobs?: number
}

export interface AdminAgentSessionListResp {
  items: AdminAgentSession[]
  total: number
}

/** Derive a display container state from the raw server fields. */
export function deriveContainerState(s: AdminAgentSession): ContainerState {
  if (s.container_cleanup_pending) return 'pending_removal'
  if (s.container_stop_pending) return 'pending_stop'
  if (s.container_stopped_at) return 'stopped'
  if (s.container_id) return 'running'
  return 'none'
}
