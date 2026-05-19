export interface AutomationTask {
  name: string
  schedule: string
  issue: {
    title: string
    body: string
    labels: string[]
  }
  roles: string[]
  enabled: boolean
}

export type AutomationRunStatus = 'running' | 'success' | 'failed'

export interface AutomationRun {
  id: number
  repo_id: number
  task_name: string
  issue_id: number | null
  issue_number: number | null
  status: AutomationRunStatus
  error_message: string
  started_at: string
  finished_at: string | null
  created_at: string
}

export interface AutomationConfig {
  tasks: AutomationTask[]
  runs: AutomationRun[]
}
