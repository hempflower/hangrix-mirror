-- +goose Up
-- Workflow system tables: runs, job runs, and logs.
-- These are independent of agent_sessions — workflow is a separate
-- execution model that reuses only the runner channel.

CREATE TABLE workflow_runs (
    id              BIGSERIAL PRIMARY KEY,
    repo_id         BIGINT NOT NULL,
    workflow_name   TEXT NOT NULL,
    source_file     TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'running', 'success', 'failed', 'cancelled')),
    event_name      TEXT NOT NULL
                    CHECK (event_name IN ('repo.push', 'issue.opened', 'issue.comment', 'workflow.dispatch')),
    cause_id        BIGINT,
    ref             TEXT NOT NULL DEFAULT '',
    commit_sha      TEXT NOT NULL DEFAULT '',
    container_snapshot_json JSONB,
    trigger_payload_json    JSONB,
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_workflow_runs_repo_status ON workflow_runs (repo_id, status);
CREATE INDEX idx_workflow_runs_repo_name ON workflow_runs (repo_id, workflow_name);

CREATE TABLE workflow_job_runs (
    id                BIGSERIAL PRIMARY KEY,
    workflow_run_id   BIGINT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    job_key           TEXT NOT NULL,
    display_name      TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'running', 'success', 'failed', 'skipped', 'cancelled')),
    sequence_index    INT NOT NULL DEFAULT 0,
    working_directory TEXT NOT NULL DEFAULT '/workspace',
    timeout_minutes   INT NOT NULL DEFAULT 60,
    runner_id         BIGINT,
    container_id      TEXT,
    env_json          JSONB,
    steps_json        JSONB,
    started_at        TIMESTAMPTZ,
    finished_at       TIMESTAMPTZ,
    exit_code         INT,
    error_message     TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_workflow_job_runs_run ON workflow_job_runs (workflow_run_id);
CREATE INDEX idx_workflow_job_runs_status ON workflow_job_runs (status);
CREATE INDEX idx_workflow_job_runs_claim ON workflow_job_runs (status, sequence_index, created_at, id)
    WHERE status = 'pending';

CREATE TABLE workflow_job_logs (
    id                  BIGSERIAL PRIMARY KEY,
    workflow_job_run_id BIGINT NOT NULL REFERENCES workflow_job_runs(id) ON DELETE CASCADE,
    stream              TEXT NOT NULL
                        CHECK (stream IN ('stdout', 'stderr', 'system')),
    line                TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_workflow_job_logs_job ON workflow_job_logs (workflow_job_run_id, created_at);

-- +goose Down
DROP TABLE IF EXISTS workflow_job_logs;
DROP TABLE IF EXISTS workflow_job_runs;
DROP TABLE IF EXISTS workflow_runs;
