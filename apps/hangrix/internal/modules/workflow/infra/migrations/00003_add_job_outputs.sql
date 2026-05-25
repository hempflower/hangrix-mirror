-- +goose Up
-- Add JSONB columns for step and job outputs in workflow_job_runs.
-- step_outputs_json: map of step_id -> {key: value} captured during job execution.
-- job_outputs_json: resolved outputs (map of key -> value) computed after job completion.
-- job_outputs_raw_json: raw output templates (map of key -> ${{ }} expression string)
--   stored at run creation so the service can resolve them at job completion.

ALTER TABLE workflow_job_runs
    ADD COLUMN step_outputs_json JSONB;

ALTER TABLE workflow_job_runs
    ADD COLUMN job_outputs_json JSONB;

ALTER TABLE workflow_job_runs
    ADD COLUMN job_outputs_raw_json JSONB;

-- +goose Down
ALTER TABLE workflow_job_runs DROP COLUMN IF EXISTS job_outputs_raw_json;
ALTER TABLE workflow_job_runs DROP COLUMN IF EXISTS job_outputs_json;
ALTER TABLE workflow_job_runs DROP COLUMN IF EXISTS step_outputs_json;
