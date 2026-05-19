-- +goose Up
CREATE TABLE automation_runs (
    id            BIGSERIAL PRIMARY KEY,
    repo_id       BIGINT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    task_name     TEXT NOT NULL,
    issue_id      BIGINT,
    status        TEXT NOT NULL DEFAULT 'running',
    error_message TEXT,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_automation_runs_repo_task
    ON automation_runs(repo_id, task_name, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS automation_runs;
