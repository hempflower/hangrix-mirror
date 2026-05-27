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
  container_state?: ContainerState
  container_last_used_at?: string | null
  container_stopped_at?: string | null
}

export type ContainerState = 'none' | 'running' | 'stopped' | 'pending_stop' | 'pending_removal'

export interface AdminAgentSessionListResp {
  items: AdminAgentSession[]
  total: number
}
