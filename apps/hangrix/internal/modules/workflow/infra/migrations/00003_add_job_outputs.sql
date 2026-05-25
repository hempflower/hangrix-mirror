-- +goose Up
-- Add step-level and job-level outputs columns to workflow_job_runs.
ALTER TABLE workflow_job_runs
    ADD COLUMN step_outputs_json JSONB,
    ADD COLUMN job_outputs_json JSONB,
    ADD COLUMN job_outputs_raw_json JSONB;

-- +goose Down
ALTER TABLE workflow_job_runs
    DROP COLUMN IF EXISTS step_outputs_json,
    DROP COLUMN IF EXISTS job_outputs_json,
    DROP COLUMN IF EXISTS job_outputs_raw_json;
