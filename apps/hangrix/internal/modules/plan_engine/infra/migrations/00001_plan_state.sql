-- +goose Up
CREATE TABLE plan_state (
  epic_issue_id    BIGINT PRIMARY KEY REFERENCES issues(id) ON DELETE CASCADE,
  status           TEXT NOT NULL DEFAULT 'active',
  max_concurrency  INT  NOT NULL DEFAULT 1,
  auto_step_budget INT  NOT NULL DEFAULT 10,
  auto_steps_used  INT  NOT NULL DEFAULT 0,
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE plan_state;
