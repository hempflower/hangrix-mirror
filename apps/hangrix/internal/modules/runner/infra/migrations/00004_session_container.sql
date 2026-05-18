-- +goose Up

-- Container lifecycle decoupled from agent-process lifecycle.
--
-- Before this migration, `docker run --rm hangrix-agent` wired container
-- lifetime to the agent process: container died on every clean exit, so
-- the next trigger on the same session had to re-pull the image and
-- re-create the workspace from scratch. The new model uses a long-lived
-- container (PID 1 = `sleep infinity`) and `docker exec`s the agent into
-- it for each run, so container state (cached deps, build artefacts,
-- partially-edited files) persists across triggers on the same session.
--
-- Three columns drive the new lifecycle:
--
--   container_id              — the runner-assigned docker container id.
--                               Empty string means "no live container"
--                               (fresh session, or container reaped). The
--                               runner populates this the first time it
--                               creates a container for the session, and
--                               clears it after a successful `docker rm`.
--
--   container_last_used_at    — bumped every time the runner exec's into
--                               the container. The 7-day idle reaper
--                               compares this against NOW() to flag stale
--                               containers for cleanup.
--
--   container_cleanup_pending — set TRUE when the platform wants the
--                               runner to docker-rm this session's
--                               container: archive (issue closed/merged),
--                               user-initiated delete, or 7-day idle
--                               sweep. The runner polls a cleanup-tasks
--                               endpoint, removes the container, and
--                               clears the flag + container_id.
--
-- The partial index narrows the per-runner cleanup poll to just the rows
-- that need work — without it, the runner would scan the full sessions
-- table on every poll.
ALTER TABLE agent_sessions
    ADD COLUMN container_id              TEXT        NOT NULL DEFAULT '',
    ADD COLUMN container_last_used_at    TIMESTAMPTZ,
    ADD COLUMN container_cleanup_pending BOOLEAN     NOT NULL DEFAULT FALSE;

CREATE INDEX agent_sessions_cleanup_idx
    ON agent_sessions (runner_id)
    WHERE container_cleanup_pending = TRUE AND container_id <> '';

-- Partial index drives the idle sweep: scan only sessions that have a
-- live container, are not already flagged, and have been touched at
-- least once. The reaper's WHERE then filters by container_last_used_at
-- against NOW() - 7 days.
CREATE INDEX agent_sessions_container_idle_idx
    ON agent_sessions (container_last_used_at)
    WHERE container_id <> '' AND container_cleanup_pending = FALSE;

-- +goose Down

DROP INDEX IF EXISTS agent_sessions_container_idle_idx;
DROP INDEX IF EXISTS agent_sessions_cleanup_idx;

ALTER TABLE agent_sessions
    DROP COLUMN IF EXISTS container_cleanup_pending,
    DROP COLUMN IF EXISTS container_last_used_at,
    DROP COLUMN IF EXISTS container_id;
