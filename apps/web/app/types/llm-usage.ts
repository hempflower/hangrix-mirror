export interface LLMUsage {
  id: number
  session_id?: number
  provider_id: number
  provider_name: string
  model: string
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  latency_ms: number
  status_code: number
  error_message?: string
  request_path?: string
  created_at: string
}

export interface LLMUsageDetail {
  id: number
  provider_name: string
  model: string
  created_at: string
  status_code: number
  request_body: string
  response_body: string
}

export interface LLMUsageListResp {
  items: LLMUsage[]
  total: number
}
