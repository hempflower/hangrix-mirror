import type { ActorRef } from './actor'
export type IssueState = 'open' | 'merged' | 'closed'

export interface Issue {
  id: number
  repo_id: number
  number: number
  author_id: number
  author_username: string
  // actor is the unified actor object (preferred). When absent the
  // frontend falls back to author_username for backward compatibility.
  actor?: ActorRef
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
  // todos and todo_summary are embedded in the issue detail response
  // (GET /api/repos/{owner}/{name}/issues/{number}). Absent means the backend
  // doesn't support todos yet — treat as "no todos".
  todos?: TodoItem[]
  todo_summary?: TodoSummary
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
  // DEPRECATED: prefer actor (unified actor model). Fall back to these
  // legacy fields when actor is absent.
  agent_role?: string
  // actor is the unified actor object (preferred).
  actor?: ActorRef
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
  | 'contribution_pushed'
  | 'contribution_merged'
  | 'contribution_rejected'
  | 'contribution_closed'

export interface IssueEvent {
  id: number
  issue_id: number
  kind: IssueEventKind
  payload: Record<string, any>
  actor_id: number
  actor_username: string
  // agent_role is set on agent-authored events (e.g. review_vote posted by
  // the reviewer role). Empty for human / system events.
  // DEPRECATED: prefer actor (unified actor model). Fall back to these
  // legacy fields when actor is absent.
  agent_role?: string
  // actor is the unified actor object (preferred).
  actor?: ActorRef
  created_at: string
}

export type ReviewVoteValue = 'approve' | 'reject' | 'abstain'

export interface ReviewVotePayload {
  value: ReviewVoteValue
  reason?: string
  // head_sha is the issue head commit SHA at the moment the vote was cast.
  // The server populates it from issue.HeadSHA before persisting the event.
  // A vote is "stale" when this no longer equals the issue's current head_sha.
  head_sha?: string
  // contribution_id / reviewed_sha are populated when the vote targets a
  // specific contribution branch rather than the issue head. Optional for
  // backward compatibility with issue-level votes.
  contribution_id?: number
  reviewed_sha?: string
}

export type ReviewVerdict = 'approved' | 'rejected' | 'pending'

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
  block_reason: string   // server-computed reason code, e.g. '' | 'review_required'
  // required_reviewers is the set of reviewer role keys that must vote before
  // the review gate can pass. pending_reviewers is the subset that have not
  // yet voted. Both are server-computed.
  required_reviewers: string[]
  pending_reviewers: string[]
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

// --- Contributions (git-branch-based merge requests) ---

export type ContributionStatus =
  | 'pending'
  | 'approved'
  | 'rejected'
  | 'merged'
  | 'closed'

// Contribution is a git-branch-based merge request pushed by an agent (or a
// human) against an issue. It replaces the old text-patch submission model:
// instead of storing a unified diff, the server tracks a real branch ref and
// computes the diff against the issue base on demand.
export interface Contribution {
  id: number
  issue_id: number
  session_id: number
  agent_role: string          // role key, e.g. "server"
  // actor is the unified actor object (preferred). When absent the
  // frontend falls back to agent_role for backward compatibility.
  actor?: ActorRef
  ref_name: string            // "refs/heads/issue-5/server"
  head_sha: string
  base_sha: string
  title: string
  description: string
  status: ContributionStatus
  mergeable: boolean
  // merge_mode: 'fast-forward' | 'merge-commit' | 'up-to-date' | 'conflicted' | ''
  merge_mode: string
  changed_paths: string[]
  files: number
  additions: number
  deletions: number
  merged_commit_sha: string
  merged_at: string | null
  created_at: string
  updated_at: string
}

// ContributionPushedPayload is the timeline event emitted when a contribution
// branch is pushed.
export interface ContributionPushedPayload {
  contribution_id: number
  agent_role: string
  ref_name: string
  head_sha: string
  title: string
}

export interface ContributionMergedPayload {
  contribution_id: number
  agent_role: string
  ref_name: string
  title: string
  merge_commit_sha: string
}

export interface ContributionRejectedPayload {
  contribution_id: number
  agent_role: string
  ref_name: string
  reason: string
}

export interface ContributionClosedPayload {
  contribution_id: number
  agent_role: string
  ref_name: string
}

export interface IssueAttachment {
  id: number
  repo_id: number
  issue_id: number
  comment_id: number
  author_id: number | null
  agent_role: string
  // actor is the unified actor object (preferred).
  actor?: ActorRef
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

/** Platform-level attachment returned by POST /api/attachments. */
export interface PlatformAttachment {
  id: number
  url: string
  markdown_snippet: string
  display_name?: string
  original_name?: string
  kind?: string
  size_bytes?: number
}


export type TodoStatus = 'todo' | 'in_progress' | 'done'

export interface TodoItem {
  id: number
  issue_id: number
  content: string
  status: TodoStatus
  position: number
  created_at: string
  updated_at: string
}

export interface TodoSummary {
  total: number
  todo: number
  in_progress: number
  done: number
  all_done: boolean
}

