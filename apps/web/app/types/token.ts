export type TokenScope = 'repo:read' | 'repo:write'

export interface PublicToken {
  id: number
  name: string
  prefix: string
  scopes: TokenScope[]
  last_used_at?: string | null
  expires_at?: string | null
  revoked_at?: string | null
  created_at: string
}

export interface TokenListResp {
  items: PublicToken[]
}

export interface TokenCreateResp {
  token: PublicToken
  plaintext: string
}
