import type { ActorRef } from './actor'
// Workflow types — kept in sync with the server-side workflow domain model
// (apps/hangrix/internal/modules/workflow/domain/).
//
// Reference: docs/workflow-system.md

export type WorkflowRunStatus = 'pending' | 'running' | 'success' | 'failed' | 'cancelled'

export type WorkflowJobStatus = 'pending' | 'running' | 'success' | 'failed' | 'skipped' | 'cancelled'

export type WorkflowEventName = 'repo.push' | 'issue.opened' | 'issue.comment' | 'workflow.dispatch' | 'repo.push_tag'

export interface WorkflowDispatchInput {
  name: string
  required: boolean
}

export interface WorkflowDefinition {
  name: string
  source_file: string
  on: WorkflowEventName[]
  dispatch_inputs: WorkflowDispatchInput[]
}

export interface WorkflowRun {
  id: number
  repo_id: number
  workflow_name: string
  source_file: string
  status: WorkflowRunStatus
  event_name: WorkflowEventName
  cause_id: number | null
  // trigger_actor is the actor that triggered this run (e.g. a user, agent, or another workflow).
  trigger_actor?: ActorRef
  // run_actor is the workflow-as-actor identity used when this run produces side effects.
  run_actor?: ActorRef
  ref: string
  commit_sha: string
  started_at: string | null
  finished_at: string | null
  created_at: string
}

export interface WorkflowJobRun {
  id: number
  workflow_run_id: number
  job_key: string
  display_name: string
  status: WorkflowJobStatus
  sequence_index: number
  working_directory: string
  timeout_minutes: number
  runner_id: number | null
  container_id: string | null
  started_at: string | null
  finished_at: string | null
  exit_code: number | null
  error_message: string
  created_at: string
  steps?: WorkflowStep[]
  step_outputs?: Record<string, Record<string, WorkflowOutputValue>>
  job_outputs?: Record<string, WorkflowOutputValue>
}

export interface WorkflowJobLogLine {
  id: number
  workflow_job_run_id: number
  stream: 'stdout' | 'stderr' | 'system'
  line: string
  created_at: string
}

export interface WorkflowOutputValue {
  value: string
  masked: boolean
}

export interface WorkflowRunDetail {
  run: WorkflowRun
  jobs: WorkflowJobRun[]
}

export interface WorkflowRunListResp {
  items: WorkflowRun[]
  total: number
}

export interface WorkflowJobLogsResp {
  lines: WorkflowJobLogLine[]
  total: number
}

/** CheckItem is a single CI check status entry, as returned by the
 *  issue-level checks endpoint (GET /api/repos/{owner}/{name}/issues/{n}/checks).
 *  Kept in sync with apps/hangrix/internal/modules/workflow/domain/workflow.go. */
export interface CheckItem {
  name: string
  status: string // "pending" | "running" | "completed"
  conclusion: string // "" | "success" | "failure" | "cancelled"
  run_id: number
  url?: string
}

export interface WorkflowStep {
  id?: string
  name: string
  type: string
  run?: string
  script?: string
  env?: Record<string, string>
  dir?: string
  with?: Record<string, any>
}
