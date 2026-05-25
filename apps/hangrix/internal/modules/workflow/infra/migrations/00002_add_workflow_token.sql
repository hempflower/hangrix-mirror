-- +goose Up
-- Add workflow_token column for short-term tokens that authenticate
-- workflow-job steps against repo-scoped write endpoints (e.g. releases).
ALTER TABLE workflow_runs
    ADD COLUMN workflow_token TEXT NOT NULL DEFAULT '';

-- Only non-empty tokens need to be unique; empty is the backward-compat
-- default for runs created before this migration.
CREATE UNIQUE INDEX idx_workflow_runs_token
    ON workflow_runs (workflow_token)
    WHERE workflow_token <> '';

-- +goose Down
DROP INDEX IF EXISTS idx_workflow_runs_token;
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS workflow_token;
