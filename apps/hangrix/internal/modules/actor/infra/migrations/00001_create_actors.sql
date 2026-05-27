-- +goose Up
-- Baseline migration for the actors table. IF NOT EXISTS is intentional:
-- this is a one-time schema-boot migration that creates the foundational
-- identity table for the entire platform. Modules loaded later (issue,
-- contribution, etc.) will reference actors(id) via FK. Because goose
-- tracks per-module version tables independently, a re-run in a test
-- environment that partially applied earlier migrations is safe —
-- IF NOT EXISTS prevents a duplicate-table error while goose still
-- records the version. Future actor-module migrations MUST NOT use
-- IF [NOT] EXISTS guards.

CREATE TABLE IF NOT EXISTS actors (
    id               BIGSERIAL    PRIMARY KEY,
    kind             TEXT         NOT NULL CHECK (kind IN
                         ('user','agent_session','agent_role','workflow_run','bot','system')),
    display_name     TEXT         NOT NULL,

    -- Payload columns: exactly one is non-null per kind, enforced by CHECK.
    user_id          BIGINT       REFERENCES users(id)              ON DELETE RESTRICT,
    agent_session_id BIGINT       REFERENCES agent_sessions(id)     ON DELETE RESTRICT,
    workflow_run_id  BIGINT       REFERENCES workflow_runs(id)      ON DELETE RESTRICT,
    repo_id          BIGINT       REFERENCES repos(id)              ON DELETE CASCADE,
    role_key         TEXT         NOT NULL DEFAULT '',
    bot_handle       TEXT         NOT NULL DEFAULT '',

    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT actors_kind_payload CHECK (
        (kind='user'           AND user_id IS NOT NULL          AND agent_session_id IS NULL AND workflow_run_id IS NULL AND role_key='' AND bot_handle='') OR
        (kind='agent_session'  AND agent_session_id IS NOT NULL AND user_id IS NULL          AND workflow_run_id IS NULL AND role_key='' AND bot_handle='') OR
        (kind='agent_role'     AND repo_id IS NOT NULL          AND role_key <> ''           AND user_id IS NULL AND agent_session_id IS NULL AND workflow_run_id IS NULL AND bot_handle='') OR
        (kind='workflow_run'   AND workflow_run_id IS NOT NULL  AND user_id IS NULL AND agent_session_id IS NULL AND role_key='' AND bot_handle='') OR
        (kind='bot'            AND bot_handle <> ''             AND user_id IS NULL AND agent_session_id IS NULL AND workflow_run_id IS NULL AND role_key='') OR
        (kind='system'         AND user_id IS NULL AND agent_session_id IS NULL AND workflow_run_id IS NULL AND role_key='' AND bot_handle='')
    )
);

-- Partial unique indexes: one per kind so ON CONFLICT DO NOTHING works
-- on the natural-key column(s) without colliding on NULLs from other kinds.
CREATE UNIQUE INDEX IF NOT EXISTS actors_user_uq           ON actors (user_id)            WHERE kind='user';
CREATE UNIQUE INDEX IF NOT EXISTS actors_agent_session_uq  ON actors (agent_session_id)   WHERE kind='agent_session';
CREATE UNIQUE INDEX IF NOT EXISTS actors_agent_role_uq     ON actors (repo_id, role_key)  WHERE kind='agent_role';
CREATE UNIQUE INDEX IF NOT EXISTS actors_workflow_uq       ON actors (workflow_run_id)    WHERE kind='workflow_run';
CREATE UNIQUE INDEX IF NOT EXISTS actors_bot_uq            ON actors (bot_handle)         WHERE kind='bot';

-- Seed the singleton system actor (id=1). It is the fallback owner for
-- actions with no human principal (e.g. cron, startup events, issue 233
-- path). Application code never Ensures this row — it assumes id=1 always
-- exists after migration.
INSERT INTO actors (id, kind, display_name)
VALUES (1, 'system', 'System')
ON CONFLICT DO NOTHING;

-- Advance the sequence past the seed so the next Ensure* call doesn't
-- collide with id=1.
SELECT setval('actors_id_seq', GREATEST(1, (SELECT COALESCE(MAX(id), 0) FROM actors)));

-- Backfill: user actors from the users table.
INSERT INTO actors (kind, display_name, user_id, created_at)
SELECT 'user', username, id, created_at FROM users
ON CONFLICT DO NOTHING;

-- Backfill: agent_role actors from historical agent_role columns across
-- issues, issue_comments, issue_events, issue_attachments, and contributions.
-- Server-reviewer item (b): all 5 tables are included so no historical
-- role attribution is lost.
INSERT INTO actors (kind, display_name, repo_id, role_key, created_at)
SELECT DISTINCT 'agent_role',
       '@agent-' || agent_role,
       repo_id,
       agent_role,
       MIN(created_at)
FROM (
    SELECT repo_id, agent_role, created_at FROM issues          WHERE agent_role <> ''
    UNION ALL
    SELECT i.repo_id, c.agent_role, c.created_at
    FROM issue_comments c JOIN issues i ON i.id = c.issue_id
    WHERE c.agent_role <> ''
    UNION ALL
    SELECT i.repo_id, e.agent_role, e.created_at
    FROM issue_events e JOIN issues i ON i.id = e.issue_id
    WHERE e.agent_role <> ''
    UNION ALL
    SELECT i.repo_id, a.agent_role, a.created_at
    FROM issue_attachments a JOIN issues i ON i.id = a.issue_id
    WHERE a.agent_role <> ''
    UNION ALL
    SELECT repo_id, agent_role, created_at FROM contributions WHERE agent_role <> ''
) x
GROUP BY repo_id, agent_role
ON CONFLICT DO NOTHING;

-- Backfill: agent_session actors.
INSERT INTO actors (kind, display_name, agent_session_id, created_at)
SELECT 'agent_session',
       COALESCE(NULLIF(role, ''), '?') || ' #' || id,
       id,
       created_at
FROM agent_sessions
ON CONFLICT DO NOTHING;

-- +goose Down
DROP INDEX IF EXISTS actors_bot_uq;
DROP INDEX IF EXISTS actors_workflow_uq;
DROP INDEX IF EXISTS actors_agent_role_uq;
DROP INDEX IF EXISTS actors_agent_session_uq;
DROP INDEX IF EXISTS actors_user_uq;
DROP TABLE IF EXISTS actors;
