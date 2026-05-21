// Workflow types — kept in sync with the server-side workflow domain model
// (apps/hangrix/internal/modules/workflow/domain/).
//
// Reference: docs/workflow-system.md

export type WorkflowRunStatus = 'pending' | 'running' | 'success' | 'failed' | 'cancelled'

export type WorkflowJobStatus = 'pending' | 'running' | 'success' | 'failed' | 'skipped' | 'cancelled'

export type WorkflowEventName = 'repo.push' | 'issue.opened' | 'issue.comment' | 'workflow.dispatch'

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
}

export interface WorkflowJobLogLine {
  id: number
  workflow_job_run_id: number
  stream: 'stdout' | 'stderr' | 'system'
  line: string
  created_at: string
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
