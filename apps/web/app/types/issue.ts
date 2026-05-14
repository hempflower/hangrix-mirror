export type IssueState = 'open' | 'merged' | 'closed'

export interface Issue {
  id: number
  repo_id: number
  number: number
  author_id: number
  author_username: string
  title: string
  body: string
  state: IssueState
  branch_name: string
  base_branch: string
  head_sha: string
  parent_number: number
  merge_commit_sha: string
  merged_at?: string | null
  created_at: string
  updated_at: string
}

export interface IssueListResp {
  items: Issue[]
  total: number
}

export interface IssueComment {
  id: number
  issue_id: number
  author_id: number
  author_username: string
  body: string
  file_path: string
  line: number
  created_at: string
  updated_at: string
}

export type IssueEventKind =
  | 'commit_pushed'
  | 'branch_merged'
  | 'state_changed'
  | 'title_changed'

export interface IssueEvent {
  id: number
  issue_id: number
  kind: IssueEventKind
  payload: Record<string, any>
  actor_id: number
  actor_username: string
  created_at: string
}

export interface IssueTimeline {
  comments: IssueComment[]
  events: IssueEvent[]
}

export interface IssueMergeResp {
  issue: Issue
  merge_sha: string
  mode: 'fast-forward' | 'merge-commit' | 'up-to-date'
}

// CommitPushedPayload mirrors the Go domain.CommitPushedPayload — kept in
// sync with apps/hangrix/internal/modules/issue/domain/issue.go.
export interface CommitPushedPayload {
  old_sha: string
  new_sha: string
  commits: Array<{
    sha: string
    message: string
    author_name: string
    committed_at: string
  }>
}

export interface BranchMergedPayload {
  into_branch: string
  from_branch: string
  merge_sha: string
  mode: string
}

export interface StateChangedPayload {
  from: IssueState
  to: IssueState
}

export interface TitleChangedPayload {
  from: string
  to: string
}
