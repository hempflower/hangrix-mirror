export interface DashboardSummary {
  total_calls: number
  total_tokens: number
  active_sessions: number
  online_runners: number
  total_runners: number
  failed_calls: number
}

export interface DailyCallsPoint {
  date: string
  count: number
}

export interface DailyTokensPoint {
  date: string
  total_tokens: number
  prompt_tokens: number
  completion_tokens: number
}

export interface ProviderRank {
  provider_name: string
  calls: number
  total_tokens: number
}

export interface DashboardHealth {
  online_runners: number
  offline_runners: number
  disabled_runners: number
  live_sessions: number
}

export interface RecentFailure {
  id: number
  provider_name: string
  model: string
  status_code: number
  error_message: string
  created_at: string
  session_id?: number
}

export interface DashboardResponse {
  summary: DashboardSummary
  timeseries: {
    daily_calls: DailyCallsPoint[]
    daily_tokens: DailyTokensPoint[]
  }
  providers: ProviderRank[]
  health: DashboardHealth
  recent_failures: RecentFailure[]
}
