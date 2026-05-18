export type RunnerVisibility = 'platform' | 'user'
export type RunnerStatus = 'pending' | 'active' | 'disabled'

export interface Runner {
  id: number
  name: string
  owner_user_id?: number | null
  visibility: RunnerVisibility
  status: RunnerStatus
  // online is a server-derived liveness flag: true when status === 'active'
  // and the last heartbeat is within ~60s of the server clock. Status alone
  // only flips on admin action, so use this to render offline state for an
  // admin-active runner that's stopped beating.
  online: boolean
  capabilities: Record<string, unknown>
  last_heartbeat_at?: string | null
  enroll_token_prefix: string
  enroll_token_used: boolean
  agent_token_prefix?: string
  agent_token_revoked: boolean
  created_by: number
  created_at: string
  updated_at: string
}

export interface RunnerListResp {
  items: Runner[]
}

export interface RunnerCreateReq {
  name: string
  visibility?: RunnerVisibility
}

export interface RunnerCreateResp {
  runner: Runner
  enroll_token: string
}
