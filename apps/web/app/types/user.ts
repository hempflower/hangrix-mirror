export type Role = 'user' | 'admin'

export interface User {
  id: number
  username: string
  email: string
  role: Role
  disabled: boolean
  created_at: string
}

export interface UserListResp {
  items: User[]
  total: number
}
