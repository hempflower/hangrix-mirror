-- +goose Up
ALTER TABLE agent_sessions
  ADD COLUMN container_stop_pending BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN container_stopped_at   TIMESTAMPTZ,
  ADD COLUMN running_jobs           INT NOT NULL DEFAULT 0;

-- Drives the runner's stop poll. Mirrors the existing cleanup index;
-- partial so the per-runner poll stays O(flagged-on-this-runner).
CREATE INDEX agent_sessions_stop_idx
  ON agent_sessions (runner_id)
  WHERE container_stop_pending = TRUE AND container_id <> '';

-- +goose Down
DROP INDEX IF EXISTS agent_sessions_stop_idx;
ALTER TABLE agent_sessions
  DROP COLUMN IF EXISTS running_jobs,
  DROP COLUMN IF EXISTS container_stopped_at,
  DROP COLUMN IF EXISTS container_stop_pending;
