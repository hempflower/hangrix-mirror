// Package domain declares the workflow module's types and interfaces.
// Other modules depend only on this package; the Postgres implementation
// and HTTP handler live in sibling packages.
package domain

import (
	"context"
	"errors"
	"time"
)

// ---- status enums ----

// RunStatus is the lifecycle state of a workflow run.
type RunStatus string

const (
	RunStatusPending   RunStatus = "pending"
	RunStatusRunning   RunStatus = "running"
	RunStatusSuccess   RunStatus = "success"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCancelled RunStatus = "cancelled"
)

// Terminal returns true when the status represents a final state.
func (s RunStatus) Terminal() bool {
	return s == RunStatusSuccess || s == RunStatusFailed || s == RunStatusCancelled
}

// JobStatus is the lifecycle state of a workflow job run.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusSuccess   JobStatus = "success"
	JobStatusFailed    JobStatus = "failed"
	JobStatusSkipped   JobStatus = "skipped"
	JobStatusCancelled JobStatus = "cancelled"
)

// Terminal returns true when the job has reached a final state.
func (s JobStatus) Terminal() bool {
	return s == JobStatusSuccess || s == JobStatusFailed ||
		s == JobStatusSkipped || s == JobStatusCancelled
}

// ---- event name constants (mirror workflowsconfig for purity) ----

// EventName identifies the trigger event for a workflow run.
type EventName string

const (
	EventRepoPush         EventName = "repo.push"
	EventRepoPushTag      EventName = "repo.push_tag"
	EventIssueOpened      EventName = "issue.opened"
	EventIssueComment     EventName = "issue.comment"
	EventWorkflowDispatch EventName = "workflow.dispatch"
)

// ---- domain models ----

// WorkflowRun is a single execution of a workflow.
type WorkflowRun struct {
	ID           int64
	RepoID       int64
	WorkflowName string
	SourceFile   string
	Status       RunStatus
	EventName    EventName
	CauseID      *int64 // push event ID, comment ID, or nil for dispatch
	Ref          string
	CommitSHA    string
	// ContainerSnapshotJSON caches the resolved container info at run creation
	// time (image, build, entrypoint, volumes, env keys) for audit/retry.
	ContainerSnapshotJSON []byte
	// TriggerPayloadJSON stores event-specific metadata for audit.
	TriggerPayloadJSON []byte
	// WorkflowToken is a short-term hangrix_wf_ token generated at run
	// creation time. Workflow steps use it to authenticate against
	// repo-scoped write endpoints (e.g. releases).
	WorkflowToken string
	StartedAt     *time.Time
	FinishedAt    *time.Time
	CreatedAt     time.Time
}

// WorkflowJobRun is a single job execution within a workflow run.
type WorkflowJobRun struct {
	ID               int64
	WorkflowRunID    int64
	JobKey           string
	DisplayName      string
	Status           JobStatus
	SequenceIndex    int32
	WorkingDirectory string
	TimeoutMinutes   int32
	RunnerID         *int64
	ContainerID      *string
	// EnvJSON stores the merged env map for this job (container ← workflow ← job)
	// as a JSON blob. Secrets are not included; only non-secret values.
	EnvJSON []byte
	// StepsJSON stores the resolved step list for this job.
	StepsJSON []byte
	// StepOutputsJSON stores per-step outputs captured during job execution.
	// Map of step_id -> {key: StepOutputValue}. Written incrementally as steps complete.
	StepOutputsJSON []byte
	// JobOutputsJSON stores resolved job outputs computed after job completion.
	// Map of output_key -> StepOutputValue. Populated from ${{ }} resolution in the
	// job's declared outputs.
	JobOutputsJSON []byte
	// JobOutputsRawJSON stores the raw output templates at run creation time.
	// Map of output_key -> expression string (may contain ${{ }} references).
	// The service resolves these against runtime context at job completion.
	JobOutputsRawJSON []byte
	StartedAt         *time.Time
	FinishedAt        *time.Time
	ExitCode          *int32
	ErrorMessage      string
	CreatedAt         time.Time
}

// LogStream identifies the output stream for a log line.
type LogStream string

const (
	LogStreamStdout LogStream = "stdout"
	LogStreamStderr LogStream = "stderr"
	LogStreamSystem LogStream = "system"
)

// WorkflowJobLogLine is a single line of output from a workflow job.
type WorkflowJobLogLine struct {
	ID               int64
	WorkflowJobRunID int64
	Stream           LogStream
	Line             string
	CreatedAt        time.Time
}

// ---- container snapshot (for audit) ----

// ContainerSnapshot captures the resolved container definition at run creation
// time, frozen so subsequent config changes don't affect in-flight runs.
type ContainerSnapshot struct {
	Image      string           `json:"image"`
	Build      *BuildSpec       `json:"build,omitempty"`
	Entrypoint []string         `json:"entrypoint,omitempty"`
	EnvKeys    []string         `json:"env_keys"` // env key names only, no values
	Volumes    []VolumeSnapshot `json:"volumes,omitempty"`
}

// BuildSpec mirrors agentsconfig.Build for snapshotting.
type BuildSpec struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// VolumeSnapshot captures a named volume mount at snapshot time.
type VolumeSnapshot struct {
	Name  string `json:"name"`
	Mount string `json:"mount"`
}

// ---- input params ----

// CreateRunParams is the input bag for creating a new workflow run and its
// initial pending jobs.
type CreateRunParams struct {
	RepoID       int64
	WorkflowName string
	SourceFile   string
	EventName    EventName
	CauseID      *int64
	Ref          string
	CommitSHA    string
	// ContainerEnv is the merged container.image env from agents.yml
	ContainerEnv map[string]string
	// ContainerImage is the resolved image (or empty if build is used)
	ContainerImage string
	// ContainerBuild is the build spec from agents.yml (nil if using image)
	ContainerBuild *BuildSpec
	// ContainerEntrypoint from agents.yml
	ContainerEntrypoint []string
	// ContainerVolumes from agents.yml
	ContainerVolumes []VolumeSnapshot
	// JobDefs carries the parsed job definitions for this workflow
	JobDefs []JobDefInput
	// DispatchInputs carries the user-provided inputs for workflow.dispatch.
	// Keys are already transformed to WORKFLOW_INPUT_UPPER_SNAKE.
	DispatchInputs map[string]string
	// TriggerPayloadJSON, when non-nil, is stored verbatim in the
	// trigger_payload_json column. When nil, the infra auto-generates
	// a payload from EventName + DispatchInputs.
	TriggerPayloadJSON []byte
	// WorkflowToken is the pre-generated hangrix_wf_ token for this run.
	WorkflowToken string
}

// JobDefInput is the input bag for a single job within a new workflow run.
type JobDefInput struct {
	JobKey           string
	DisplayName      string
	Env              map[string]string
	TimeoutMinutes   int32
	WorkingDirectory string
	Steps            []StepInput
	// Outputs carries the raw output templates from the job definition.
	// Map of output_key -> expression string (may contain ${{ }} references).
	Outputs map[string]string
}

// AssetInput is a single asset to attach to a release step.
type AssetInput struct {
	// Path is the container-relative or absolute path to the file.
	Path string `json:"path"`
	// Name overrides the uploaded asset file name. When empty, the
	// basename of Path is used.
	Name string `json:"name,omitempty"`
}

// StepInput is a single step within a job definition.
type StepInput struct {
	Id   *string // optional step id for ${{ steps.<id>.outputs.<key> }} references
	Name string
	// Type discriminates between step kinds. "" and "run" are shell steps;
	// "release" is a built-in release creation step.
	Type string `json:"type,omitempty"`
	// Run is the shell command (only for type=run / type omitted).
	Run string `json:"run,omitempty"`
	// Tag is the release tag name (only for type=release).
	Tag string `json:"tag,omitempty"`
	// Notes is the release notes (only for type=release).
	Notes string `json:"notes,omitempty"`
	// Draft, when true, creates the release as a draft (only for type=release).
	// Default true.
	Draft bool `json:"draft"`
	// Assets lists files to upload to the release (only for type=release).
	Assets []AssetInput `json:"assets,omitempty"`
}

// StepOutputValue is a single output value with masking metadata.
// The runner reports which output keys contain secret values (masked=true),
// and the UI uses this to render secrets as "***".
type StepOutputValue struct {
	Value  string `json:"value"`
	Masked bool   `json:"masked"`
}

// ---- interfaces ----

// Store is the persistence abstraction for workflow runs, jobs, and logs.
type Store interface {
	// ---- workflow runs ----

	// CreateRun inserts a new workflow_run row in 'pending' status and
	// all associated workflow_job_run rows in 'pending' status.
	CreateRun(ctx context.Context, params CreateRunParams) (*WorkflowRun, []*WorkflowJobRun, error)

	// GetRun returns a single workflow run by ID.
	GetRun(ctx context.Context, id int64) (*WorkflowRun, error)

	// GetRunByToken returns the repo_id and status for a workflow run

	GetRunByToken(ctx context.Context, token string) (repoID int64, status RunStatus, err error)

	// ListRunsByRepo returns workflow runs for a repo, ordered by created_at DESC.
	// workflowName filters to a specific workflow (empty = all).
	ListRunsByRepo(ctx context.Context, repoID int64, workflowName string, offset, limit int32) ([]*WorkflowRun, int64, error)

	// MarkRunStarted transitions a run from pending to running.
	MarkRunStarted(ctx context.Context, id int64) error

	// MarkRunTerminal transitions a run to a terminal status.
	MarkRunTerminal(ctx context.Context, id int64, status RunStatus) error

	// ---- workflow job runs ----

	// GetJobRun returns a single job run by ID.
	GetJobRun(ctx context.Context, id int64) (*WorkflowJobRun, error)

	// ListJobRunsByRun returns all job runs for a workflow run, ordered by sequence_index.
	ListJobRunsByRun(ctx context.Context, workflowRunID int64) ([]*WorkflowJobRun, error)

	// ClaimNextJob claims the next pending workflow job (oldest first) for a runner.
	// Uses SELECT ... FOR UPDATE SKIP LOCKED for race-safe claiming.
	// Returns ErrNoPendingJob when no jobs are available.
	ClaimNextJob(ctx context.Context, runnerID int64) (*WorkflowJobRun, error)

	// MarkJobRunning transitions a job from pending/claimed to running.
	MarkJobRunning(ctx context.Context, id int64, runnerID int64) error

	// MarkJobTerminal transitions a job to a terminal status with exit code and message.
	MarkJobTerminal(ctx context.Context, id int64, status JobStatus, exitCode *int32, errMsg string) error

	// SkipRemainingJobs marks all remaining pending jobs in a run as skipped.
	SkipRemainingJobs(ctx context.Context, workflowRunID int64, afterSequenceIndex int32) error

	// SetJobContainer records the container ID for a running job.
	SetJobContainer(ctx context.Context, id int64, containerID string) error

	// SetStepOutputs merges a step's outputs into the job's step_outputs_json.
	// stepID identifies the step within the job (must match a declared step id).
	// outputs is the map of key -> StepOutputValue captured from the step's stdout.
	SetStepOutputs(ctx context.Context, id int64, stepID string, outputs map[string]StepOutputValue) error

	// SetJobOutputs writes resolved job outputs after job completion.
	SetJobOutputs(ctx context.Context, id int64, outputs map[string]StepOutputValue) error

	// ---- workflow job logs ----

	// AppendLog appends a single log line to a job run.
	AppendLog(ctx context.Context, jobRunID int64, stream LogStream, line string) error

	// ListLogs returns log lines for a job run, ordered by created_at ASC.
	ListLogs(ctx context.Context, jobRunID int64, offset, limit int32) ([]*WorkflowJobLogLine, int64, error)
}

// Dispatcher is the cross-module interface that the runner module uses to
// claim workflow jobs during task polling. It exposes the minimal surface
// needed for the runner to discover and claim workflow work.
type Dispatcher interface {
	// ClaimNextJob claims the next pending workflow job for the given runner.
	// Returns ErrNoPendingJob when no jobs are available.
	ClaimNextJob(ctx context.Context, runnerID int64) (*WorkflowJobRun, error)

	// GetRunForJob returns the workflow run that owns the given job.
	GetRunForJob(ctx context.Context, jobRunID int64) (*WorkflowRun, error)
}

// TagEventTrigger is the cross-module interface for triggering workflow runs
// in response to tag creation or push events. Modules that produce tag events
// (repo REST API, git push observers) depend on this interface rather than
// the concrete service.
type TagEventTrigger interface {
	TriggerTagEvent(ctx context.Context, repoID int64, ownerName, repoName, defaultBranch, tagName, commitSHA string) error
}

// ---- sentinel errors ----

// WorkflowTokenValidator is the cross-module interface that allows other
// modules (e.g. release) to validate a hangrix_wf_ token and get the repo
// ID it is scoped to. The workflow module's Service implements it.
type WorkflowTokenValidator interface {
	ValidateWorkflowToken(ctx context.Context, token string) (repoID int64, err error)
}

var (
	ErrNoPendingJob         = errors.New("no pending workflow job")
	ErrJobNotFound          = errors.New("workflow job not found")
	ErrRunNotFound          = errors.New("workflow run not found")
	ErrInvalidStatus        = errors.New("invalid status transition")
	ErrInvalidWorkflowToken = errors.New("invalid workflow token")
)
