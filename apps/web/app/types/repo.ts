export type Visibility = 'public' | 'private'

export type OwnerKind = 'user' | 'org'

export interface PublicRepo {
  id: number
  owner_kind: OwnerKind
  owner_id: number
  owner_name: string
  /** Legacy alias for `owner_name` kept until all callers migrate. */
  owner_username: string
  name: string
  description: string
  visibility: Visibility
  default_branch: string
  created_at: string
  updated_at: string
}

export interface RepoListResp {
  items: PublicRepo[]
  total: number
}

export interface RepoRef {
  name: string
  sha: string
}

export interface RepoRefs {
  default_branch: string
  default_branch_sha: string
  branches: RepoRef[]
  tags: RepoRef[]
}

export interface Signature {
  name: string
  email: string
  when: string
}

export interface Commit {
  sha: string
  parent_shas: string[]
  author: Signature
  committer: Signature
  message: string
  committed_at: string
}

export type EntryKind = 'blob' | 'executable' | 'tree' | 'symlink' | 'submodule'

export interface TreeEntry {
  name: string
  path: string
  kind: EntryKind
  sha: string
  size: number
}

export interface EntryWithCommit extends TreeEntry {
  last_commit?: Commit
}

export interface TreeView {
  entries: EntryWithCommit[]
  last_commit?: Commit
  total_commits: number
}

export type DiffStatus = 'added' | 'modified' | 'deleted' | 'renamed'

export interface FileDiff {
  old_path: string
  new_path: string
  status: DiffStatus
  patch: string
  binary: boolean
}

export interface CommitWithDiff {
  commit: Commit
  diff: FileDiff[]
}

export interface BlobResp {
  content_base64: string
  binary: boolean
  size: number
}

export interface BranchProtection {
  id: number
  repo_id: number
  pattern: string
  forbid_force_push: boolean
  forbid_delete: boolean
  forbid_direct_push: boolean
  created_at: string
  updated_at: string
}

export interface ContainingRefs {
  branches: RepoRef[]
  tags: RepoRef[]
}

export type RepoMemberRole = 'read' | 'write'

export interface RepoMember {
  user_id: number
  username: string
  role: RepoMemberRole
  added_at: string
  added_by: number
}

export interface RepoMemberListResp {
  items: RepoMember[]
}
