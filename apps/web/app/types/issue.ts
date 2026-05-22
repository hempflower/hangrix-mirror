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
  // review_status is the server-computed review gate summary. Absent / null
  // means the backend hasn't computed it yet — treat as "pending, no block".
  review_status?: ReviewStatus | null
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
  | 'patch_submitted'
  | 'patch_applied'
  | 'patch_rejected'

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
  // head_sha is the issue head commit SHA at the moment the vote was cast.
  // The server populates it from issue.HeadSHA before persisting the event.
  // A vote is "stale" when this no longer equals the issue's current head_sha.
  head_sha?: string
}

export type ReviewVerdict = 'approved' | 'changes_requested' | 'pending'

// ReviewVoteEntry is a single valid (current-head_sha) review vote. The
// reviewer string carries the agent_role key or a human username; the
// frontend renders agent roles with the @agent- prefix.
export interface ReviewVoteEntry {
  reviewer: string       // agent_role key or username
  value: ReviewVoteValue
  reason: string
  voted_at: string
}

// StaleVoteEntry is a vote that was cast against an older head_sha and no
// longer counts toward the merge gate.
export interface StaleVoteEntry {
  reviewer: string
  value: ReviewVoteValue
  vote_head_sha: string
  voted_at: string
}

// ReviewStatus is the server-computed review gate summary. The server is the
// single source of truth; the frontend must not derive review state from the
// raw timeline. Absent / null means the backend hasn't computed it yet
// (forward-compat — treat as "pending, no block").
export interface ReviewStatus {
  head_sha: string
  verdict: ReviewVerdict
  merge_blocked: boolean
  block_reason: string   // '' | 'review_required' | 'changes_requested'
  votes: ReviewVoteEntry[]
  stale_votes: StaleVoteEntry[]
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

// --- Patch Submissions ---

export type PatchStatus = 'submitted' | 'stale' | 'applied' | 'rejected' | 'superseded'

export interface IssuePatchSubmission {
  id: number
  repo_id: number
  issue_id: number
  session_id: number
  agent_role: string
  base_head_sha: string
  title: string
  description: string
  patch_text: string
  changed_paths: string[]
  file_count: number
  additions: number
  deletions: number
  status: PatchStatus
  applied_commit_sha: string
  applied_at: string | null
  rejected_reason: string
  created_at: string
  updated_at: string
  // Server-parsed per-file diffs for rendering (available on detail endpoint)
  files?: import('~/types/repo').FileDiff[]
}

export interface PatchSubmittedPayload {
  submission_id: number
  title: string
  base_head_sha: string
  changed_paths: string[]
  file_count: number
  additions: number
  deletions: number
}

export interface PatchAppliedPayload {
  submission_id: number
  commit_sha: string
}

export interface PatchRejectedPayload {
  submission_id: number
  reason: string
}

export interface IssueAttachment {
  id: number
  repo_id: number
  issue_id: number
  comment_id: number
  author_id: number | null
  agent_role: string
  original_name: string
  display_name: string
  inline: boolean

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

