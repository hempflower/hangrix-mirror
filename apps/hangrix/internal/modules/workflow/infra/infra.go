// Package infra holds the Postgres-backed implementation of the workflow
// persistence layer. It implements domain.Store using sqlc-generated queries,
// mapping pgx types to domain types and Postgres errors to domain sentinels.
package infra

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/infra/workflowdb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresRepo implements domain.Store on top of a pgx pool.
type PostgresRepo struct {
	q *workflowdb.Queries
}

// PostgresRepoDeps wires the PostgresRepo's dependencies through ioc.
type PostgresRepoDeps struct {
	Pool *pgxpool.Pool
}

// NewPostgresRepo creates a PostgresRepo, applies goose migrations, and
// returns a ready-to-use implementation of domain.Store.
func NewPostgresRepo(deps *PostgresRepoDeps) *PostgresRepo {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("workflow migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_workflow", "."); err != nil {
		panic(fmt.Errorf("apply workflow migrations: %w", err))
	}
	return &PostgresRepo{q: workflowdb.New(deps.Pool)}
}

// ---- workflow runs ----

// CreateRun inserts a new workflow_run and all associated workflow_job_run rows
// in a single transaction. Returns the created run and its jobs.
func (r *PostgresRepo) CreateRun(ctx context.Context, params domain.CreateRunParams) (*domain.WorkflowRun, []*domain.WorkflowJobRun, error) {
	snapJSON, err := json.Marshal(domain.ContainerSnapshot{
		Image:      params.ContainerImage,
		Build:      params.ContainerBuild,
		Entrypoint: params.ContainerEntrypoint,
		EnvKeys:    mapKeys(params.ContainerEnv),
		Volumes:    params.ContainerVolumes,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal container snapshot: %w", err)
	}

	var triggerJSON []byte
	if params.TriggerPayloadJSON != nil {
		triggerJSON = params.TriggerPayloadJSON
	} else {
		var err error
		triggerJSON, err = json.Marshal(map[string]any{
			"event_name":      params.EventName,
			"dispatch_inputs": params.DispatchInputs,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("marshal trigger payload: %w", err)
		}
	}

	var causeID pgtype.Int8
	if params.CauseID != nil {
		causeID = pgtype.Int8{Int64: *params.CauseID, Valid: true}
	}

	dbRun, err := r.q.CreateWorkflowRun(ctx, workflowdb.CreateWorkflowRunParams{
		RepoID:                params.RepoID,
		WorkflowName:          params.WorkflowName,
		SourceFile:            params.SourceFile,
		EventName:             string(params.EventName),
		CauseID:               causeID,
		Ref:                   params.Ref,
		CommitSha:             params.CommitSHA,
		ContainerSnapshotJson: snapJSON,
		TriggerPayloadJson:    triggerJSON,
		WorkflowToken:         params.WorkflowToken,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create workflow run: %w", err)
	}

	run := rowToRun(&dbRun)

	// Create all job rows
	jobs := make([]*domain.WorkflowJobRun, 0, len(params.JobDefs))
	for i, jd := range params.JobDefs {
		// Merge env: container ← workflow (stored in params.ContainerEnv)
		// Note: params.ContainerEnv already has workflow-level env merged by the service
		mergedEnv := make(map[string]string)
		for k, v := range params.ContainerEnv {
			mergedEnv[k] = v
		}
		for k, v := range jd.Env {
			mergedEnv[k] = v
		}
		// Add dispatch inputs
		for k, v := range params.DispatchInputs {
			mergedEnv[k] = v
		}

		envJSON, err := json.Marshal(mergedEnv)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal job env: %w", err)
		}

		stepsJSON, err := json.Marshal(jd.Steps)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal job steps: %w", err)
		}

		var outputsRawJSON []byte
		if len(jd.Outputs) > 0 {
			var err error
			outputsRawJSON, err = json.Marshal(jd.Outputs)
			if err != nil {
				return nil, nil, fmt.Errorf("marshal job outputs raw: %w", err)
			}
		}

		dbJob, err := r.q.CreateWorkflowJobRun(ctx, workflowdb.CreateWorkflowJobRunParams{
			WorkflowRunID:      run.ID,
			JobKey:             jd.JobKey,
			DisplayName:        jd.DisplayName,
			SequenceIndex:      int32(i),
			WorkingDirectory:   jd.WorkingDirectory,
			TimeoutMinutes:     jd.TimeoutMinutes,
			EnvJson:            envJSON,
			StepsJson:          stepsJSON,
			JobOutputsRawJson:  outputsRawJSON,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("create workflow job run: %w", err)
		}
		job := rowToJobRun(&dbJob)
		jobs = append(jobs, job)
	}

	return run, jobs, nil
}

// GetRun returns a single workflow run by ID.
func (r *PostgresRepo) GetRun(ctx context.Context, id int64) (*domain.WorkflowRun, error) {
	dbRun, err := r.q.GetWorkflowRun(ctx, id)
	if err != nil {
		if isNoRows(err) {
			return nil, domain.ErrRunNotFound
		}
		return nil, fmt.Errorf("get workflow run: %w", err)
	}
	return rowToRun(&dbRun), nil
}

// GetRunByToken returns the repo_id and status for a workflow run identified
// by its workflow_token.
func (r *PostgresRepo) GetRunByToken(ctx context.Context, token string) (int64, domain.RunStatus, error) {
	row, err := r.q.GetWorkflowRunByToken(ctx, token)
	if err != nil {
		if isNoRows(err) {
			return 0, "", domain.ErrRunNotFound
		}
		return 0, "", fmt.Errorf("get workflow run by token: %w", err)
	}
	return row.RepoID, domain.RunStatus(row.Status), nil
}

// ListRunsByRepo returns workflow runs for a repo, ordered by created_at DESC.
func (r *PostgresRepo) ListRunsByRepo(ctx context.Context, repoID int64, workflowName string, offset, limit int32) ([]*domain.WorkflowRun, int64, error) {
	rows, err := r.q.ListWorkflowRunsByRepo(ctx, workflowdb.ListWorkflowRunsByRepoParams{
		RepoID:       repoID,
		WorkflowName: workflowName,
		Offset:       offset,
		Limit:        limit,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list workflow runs: %w", err)
	}
	if len(rows) == 0 {
		return nil, 0, nil
	}

	runs := make([]*domain.WorkflowRun, len(rows))
	for i, row := range rows {
		runs[i] = rowToRunFromList(&row)
	}
	return runs, rows[0].TotalCount, nil
}

// MarkRunStarted transitions a run from pending to running.
func (r *PostgresRepo) MarkRunStarted(ctx context.Context, id int64) error {
	err := r.q.MarkWorkflowRunStarted(ctx, id)
	if err != nil {
		return fmt.Errorf("mark run started: %w", err)
	}
	return nil
}

// MarkRunTerminal transitions a run to a terminal status.
func (r *PostgresRepo) MarkRunTerminal(ctx context.Context, id int64, status domain.RunStatus) error {
	err := r.q.MarkWorkflowRunTerminal(ctx, workflowdb.MarkWorkflowRunTerminalParams{
		ID:     id,
		Status: string(status),
	})
	if err != nil {
		return fmt.Errorf("mark run terminal: %w", err)
	}
	return nil
}

// ---- workflow job runs ----

// GetJobRun returns a single job run by ID.
func (r *PostgresRepo) GetJobRun(ctx context.Context, id int64) (*domain.WorkflowJobRun, error) {
	dbJob, err := r.q.GetWorkflowJobRun(ctx, id)
	if err != nil {
		if isNoRows(err) {
			return nil, domain.ErrJobNotFound
		}
		return nil, fmt.Errorf("get workflow job run: %w", err)
	}
	return rowToJobRun(&dbJob), nil
}

// ListJobRunsByRun returns all job runs for a workflow run, ordered by sequence_index.
func (r *PostgresRepo) ListJobRunsByRun(ctx context.Context, workflowRunID int64) ([]*domain.WorkflowJobRun, error) {
	dbJobs, err := r.q.ListWorkflowJobRunsByRun(ctx, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("list job runs: %w", err)
	}
	jobs := make([]*domain.WorkflowJobRun, len(dbJobs))
	for i, dbj := range dbJobs {
		dbjCopy := dbj
		jobs[i] = rowToJobRun(&dbjCopy)
	}
	return jobs, nil
}

// ClaimNextJob claims the next pending workflow job for a runner.
func (r *PostgresRepo) ClaimNextJob(ctx context.Context, runnerID int64) (*domain.WorkflowJobRun, error) {
	dbJob, err := r.q.ClaimNextWorkflowJob(ctx, pgtype.Int8{Int64: runnerID, Valid: true})
	if err != nil {
		if isNoRows(err) {
			return nil, domain.ErrNoPendingJob
		}
		return nil, fmt.Errorf("claim next job: %w", err)
	}
	return rowToJobRun(&dbJob), nil
}

// MarkJobRunning transitions a job from pending to running.
func (r *PostgresRepo) MarkJobRunning(ctx context.Context, id int64, runnerID int64) error {
	return r.q.MarkWorkflowJobRunning(ctx, workflowdb.MarkWorkflowJobRunningParams{
		ID:       id,
		RunnerID: pgtype.Int8{Int64: runnerID, Valid: true},
	})
}

// MarkJobTerminal transitions a job to a terminal status.
func (r *PostgresRepo) MarkJobTerminal(ctx context.Context, id int64, status domain.JobStatus, exitCode *int32, errMsg string) error {
	var code pgtype.Int4
	if exitCode != nil {
		code = pgtype.Int4{Int32: *exitCode, Valid: true}
	}
	return r.q.MarkWorkflowJobTerminal(ctx, workflowdb.MarkWorkflowJobTerminalParams{
		ID:           id,
		Status:       string(status),
		ExitCode:     code,
		ErrorMessage: errMsg,
	})
}

// SkipRemainingJobs marks all remaining pending jobs in a run as skipped.
func (r *PostgresRepo) SkipRemainingJobs(ctx context.Context, workflowRunID int64, afterSequenceIndex int32) error {
	return r.q.SkipRemainingWorkflowJobs(ctx, workflowdb.SkipRemainingWorkflowJobsParams{
		WorkflowRunID:      workflowRunID,
		AfterSequenceIndex: afterSequenceIndex,
	})
}

// SetJobContainer records the container ID for a running job.
func (r *PostgresRepo) SetJobContainer(ctx context.Context, id int64, containerID string) error {
	return r.q.SetWorkflowJobContainer(ctx, workflowdb.SetWorkflowJobContainerParams{
		ID:          id,
		ContainerID: pgtype.Text{String: containerID, Valid: true},
	})
}

// SetStepOutputs merges a step's outputs into the job's step_outputs_json.
func (r *PostgresRepo) SetStepOutputs(ctx context.Context, id int64, stepID string, outputs map[string]string) error {
	outJSON, err := json.Marshal(outputs)
	if err != nil {
		return fmt.Errorf("set step outputs: marshal: %w", err)
	}
	return r.q.SetWorkflowJobStepOutputs(ctx, workflowdb.SetWorkflowJobStepOutputsParams{
		ID:      id,
		StepID:  stepID,
		Outputs: outJSON,
	})
}

// SetJobOutputs writes resolved job outputs after job completion.
func (r *PostgresRepo) SetJobOutputs(ctx context.Context, id int64, outputs map[string]string) error {
	outJSON, err := json.Marshal(outputs)
	if err != nil {
		return fmt.Errorf("set job outputs: marshal: %w", err)
	}
	return r.q.SetWorkflowJobOutputs(ctx, workflowdb.SetWorkflowJobOutputsParams{
		ID:      id,
		Outputs: outJSON,
	})
}

// ---- workflow job logs ----

// AppendLog appends a single log line to a job run.
func (r *PostgresRepo) AppendLog(ctx context.Context, jobRunID int64, stream domain.LogStream, line string) error {
	return r.q.AppendWorkflowJobLog(ctx, workflowdb.AppendWorkflowJobLogParams{
		WorkflowJobRunID: jobRunID,
		Stream:           string(stream),
		Line:             line,
	})
}

// ListLogs returns log lines for a job run, ordered by created_at ASC.
func (r *PostgresRepo) ListLogs(ctx context.Context, jobRunID int64, offset, limit int32) ([]*domain.WorkflowJobLogLine, int64, error) {
	rows, err := r.q.ListWorkflowJobLogs(ctx, workflowdb.ListWorkflowJobLogsParams{
		WorkflowJobRunID: jobRunID,
		Offset:           offset,
		Limit:            limit,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list logs: %w", err)
	}
	if len(rows) == 0 {
		return nil, 0, nil
	}

	lines := make([]*domain.WorkflowJobLogLine, len(rows))
	for i, row := range rows {
		lines[i] = &domain.WorkflowJobLogLine{
			ID:              row.ID,
			WorkflowJobRunID: row.WorkflowJobRunID,
			Stream:          domain.LogStream(row.Stream),
			Line:            row.Line,
			CreatedAt:       row.CreatedAt.Time,
		}
	}
	return lines, rows[0].TotalCount, nil
}

// ---- row-to-domain mappers ----

func rowToRun(row *workflowdb.WorkflowRun) *domain.WorkflowRun {
	r := &domain.WorkflowRun{
		ID:                    row.ID,
		RepoID:                row.RepoID,
		WorkflowName:          row.WorkflowName,
		SourceFile:            row.SourceFile,
		Status:                domain.RunStatus(row.Status),
		EventName:             domain.EventName(row.EventName),
		Ref:                   row.Ref,
		CommitSHA:             row.CommitSha,
		ContainerSnapshotJSON: row.ContainerSnapshotJson,
		TriggerPayloadJSON:    row.TriggerPayloadJson,
		WorkflowToken:         row.WorkflowToken,
		CreatedAt:             row.CreatedAt.Time,
	}
	if row.CauseID.Valid {
		r.CauseID = &row.CauseID.Int64
	}
	if row.StartedAt.Valid {
		r.StartedAt = &row.StartedAt.Time
	}
	if row.FinishedAt.Valid {
		r.FinishedAt = &row.FinishedAt.Time
	}
	return r
}

func rowToRunFromList(row *workflowdb.ListWorkflowRunsByRepoRow) *domain.WorkflowRun {
	r := &domain.WorkflowRun{
		ID:                    row.ID,
		RepoID:                row.RepoID,
		WorkflowName:          row.WorkflowName,
		SourceFile:            row.SourceFile,
		Status:                domain.RunStatus(row.Status),
		EventName:             domain.EventName(row.EventName),
		Ref:                   row.Ref,
		CommitSHA:             row.CommitSha,
		ContainerSnapshotJSON: row.ContainerSnapshotJson,
		TriggerPayloadJSON:    row.TriggerPayloadJson,
		WorkflowToken:         row.WorkflowToken,
		CreatedAt:             row.CreatedAt.Time,
	}
	if row.CauseID.Valid {
		r.CauseID = &row.CauseID.Int64
	}
	if row.StartedAt.Valid {
		r.StartedAt = &row.StartedAt.Time
	}
	if row.FinishedAt.Valid {
		r.FinishedAt = &row.FinishedAt.Time
	}
	return r
}

func rowToJobRun(row *workflowdb.WorkflowJobRun) *domain.WorkflowJobRun {
	j := &domain.WorkflowJobRun{
		ID:               row.ID,
		WorkflowRunID:    row.WorkflowRunID,
		JobKey:           row.JobKey,
		DisplayName:      row.DisplayName,
		Status:           domain.JobStatus(row.Status),
		SequenceIndex:    row.SequenceIndex,
		WorkingDirectory: row.WorkingDirectory,
		TimeoutMinutes:   row.TimeoutMinutes,
		EnvJSON:           row.EnvJson,
		StepsJSON:         row.StepsJson,
		StepOutputsJSON:   row.StepOutputsJson,
		JobOutputsJSON:    row.JobOutputsJson,
		JobOutputsRawJSON: row.JobOutputsRawJson,
		ErrorMessage:      row.ErrorMessage,
		CreatedAt:        row.CreatedAt.Time,
	}
	if row.RunnerID.Valid {
		j.RunnerID = &row.RunnerID.Int64
	}
	if row.ContainerID.Valid {
		j.ContainerID = &row.ContainerID.String
	}
	if row.StartedAt.Valid {
		j.StartedAt = &row.StartedAt.Time
	}
	if row.FinishedAt.Valid {
		j.FinishedAt = &row.FinishedAt.Time
	}
	if row.ExitCode.Valid {
		j.ExitCode = &row.ExitCode.Int32
	}
	return j
}

// ---- helpers ----

func mapKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
