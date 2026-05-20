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
  // agent_role is set on agent-authored comments. Empty for human comments.
  // The frontend uses it to render `@agent-<role>` plus a bot avatar in the
  // timeline so agent-emitted comments aren't shown with a blank author.
  agent_role?: string
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
  | 'review_vote'

export interface IssueEvent {
  id: number
  issue_id: number
  kind: IssueEventKind
  payload: Record<string, any>
  actor_id: number
  actor_username: string
  // agent_role is set on agent-authored events (e.g. review_vote posted by
  // the reviewer role). Empty for human / system events.
  agent_role?: string
  created_at: string
}

export type ReviewVoteValue = 'approve' | 'request_changes' | 'abstain'

export interface ReviewVotePayload {
  value: ReviewVoteValue
  reason?: string
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
  base_sha?: string
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

export interface IssueAttachment {
  id: number
  repo_id: number
  issue_id: number
  comment_id: number
  author_id: number | null
  agent_role: string
  original_name: string
  size_bytes: number
  mime_type: string
  detected_mime_type: string
  sha256: string
  kind: 'image' | 'video' | 'archive' | 'text' | 'binary'
  status: 'uploaded' | 'attached' | 'deleted'
  created_at: string
  deleted_at: string | null
  download_url: string
  preview_url: string
  markdown_snippet: string
}

