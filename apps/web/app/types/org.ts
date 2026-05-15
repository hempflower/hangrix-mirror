export type OrgRole = 'owner' | 'member'

export interface PublicOrg {
  id: number
  name: string
  display_name: string
  description: string
  avatar_url: string
  created_by: number
  created_at: string
  updated_at: string
}

export interface OrgListResp {
  items: PublicOrg[]
}

export interface PublicMember {
  user_id: number
  username: string
  role: OrgRole
  added_at: string
  added_by: number
}

export interface MemberListResp {
  items: PublicMember[]
}

// Owner = the (kind, id, name) tuple a repo (or any future namespaced
// resource) lives under. The wire payload is identical to repo.OwnerKind
// in the Go domain.
export type OwnerKind = 'user' | 'org'
