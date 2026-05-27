-- +goose Up

-- Container stop lifecycle: decouple "please docker stop this container"
-- from the existing cleanup flag (which means "docker rm").
--
-- Three new columns:
--
--   container_stop_pending   — TRUE when the platform wants the runner to
--                              `docker stop` this session's container.
--                              Set by idle-stop reaper, or admin manual
--                              stop-container action. The runner polls a
--                              stop-tasks endpoint, stops the container,
--                              and ACKs via stop-tasks/{sid}/done.
--
--   container_stopped_at     — TIMESTAMPTZ recorded when the runner ACKs
--                              the stop. Cleared on resume (SetSessionContainer)
--                              so a rewoken session starts fresh.
--
--   running_jobs             — INT count of active docker exec's inside
--                              the container. The runner increments on
--                              each exec start and decrements on finish.
--                              The idle-stop reaper excludes rows with
--                              running_jobs > 0 to avoid interrupting a
--                              mid-flight agent turn.
--
-- Partial index drives the stop-tasks poll: only scan sessions whose
-- container needs stopping and actually have a live container.
ALTER TABLE agent_sessions
    ADD COLUMN container_stop_pending BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN container_stopped_at   TIMESTAMPTZ,
    ADD COLUMN running_jobs           INT       NOT NULL DEFAULT 0;

CREATE INDEX agent_sessions_stop_idx
    ON agent_sessions (runner_id)
    WHERE container_stop_pending = TRUE AND container_id <> '';

-- +goose Down

DROP INDEX IF EXISTS agent_sessions_stop_idx;

ALTER TABLE agent_sessions
    DROP COLUMN IF EXISTS running_jobs,
    DROP COLUMN IF EXISTS container_stopped_at,
    DROP COLUMN IF EXISTS container_stop_pending;
