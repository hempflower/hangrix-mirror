export type ProviderType = 'openai' | 'anthropic' | 'openai-compat'

export interface LLMProvider {
  id: number
  name: string
  type: ProviderType
  base_url: string
  has_api_key: boolean
  allowed_models: string[]
  created_by: number
  created_at: string
  updated_at: string
}

export interface LLMProviderListResp {
  items: LLMProvider[]
}

export interface LLMProviderCreateReq {
  name: string
  type: ProviderType
  base_url: string
  api_key: string
  allowed_models: string[]
}

export interface LLMProviderPatchReq {
  base_url?: string
  api_key?: string
  allowed_models?: string[]
}
