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
}

export interface AdminAgentSessionListResp {
  items: AdminAgentSession[]
}
