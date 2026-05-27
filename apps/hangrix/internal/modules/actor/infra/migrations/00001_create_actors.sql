-- +goose Up
-- Create the normalized actors table. Each row represents a unique actor
-- identity keyed by (kind, kind-specific identifier).
--
-- IF NOT EXISTS is acceptable here because this is a baseline one-time
-- migration that runs before any actor-dependent modules. On a fresh DB it
-- creates the table; on a DB that already has it (unlikely but possible
-- during cross-module coordination) it is a no-op. This is the ONLY
-- migration in this module that uses IF NOT EXISTS — future migrations
-- MUST NOT follow this pattern.
CREATE TABLE IF NOT EXISTS actors (
    id               BIGSERIAL    PRIMARY KEY,
    kind             TEXT         NOT NULL CHECK (kind IN ('user','agent','agent_session','bot','workflow','system')),
    display_name     TEXT         NOT NULL,
    user_id          BIGINT       REFERENCES users(id)           ON DELETE CASCADE,
    role_key         TEXT,
    workflow_run_id  BIGINT       REFERENCES workflow_runs(id)   ON DELETE CASCADE,
    agent_session_id BIGINT       REFERENCES agent_sessions(id)  ON DELETE CASCADE,
    bot_id           TEXT,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- Partial unique indexes enforce at-most-one-row per kind-specific key.
-- Each index covers only rows of a specific kind, so two different kinds
-- can share the same user_id / role_key / etc. without conflict.
CREATE UNIQUE INDEX IF NOT EXISTS actors_user_unique
    ON actors (kind, user_id) WHERE kind = 'user' AND user_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS actors_agent_role_unique
    ON actors (kind, role_key) WHERE kind = 'agent' AND role_key IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS actors_agent_session_unique
    ON actors (kind, agent_session_id) WHERE kind = 'agent_session' AND agent_session_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS actors_bot_unique
    ON actors (kind, bot_id) WHERE kind = 'bot' AND bot_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS actors_workflow_run_unique
    ON actors (kind, workflow_run_id) WHERE kind = 'workflow' AND workflow_run_id IS NOT NULL;

-- Seed the system actor at id=1. This row is the singleton system identity
-- used when no other actor is applicable (cron jobs, server-initiated
-- actions, cleanup sweeps).
INSERT INTO actors (id, kind, display_name)
VALUES (1, 'system', 'System')
ON CONFLICT DO NOTHING;

-- Advance the sequence past the seeded id so the next auto-assigned id is >1.
SELECT setval('actors_id_seq', GREATEST(1, (SELECT COALESCE(MAX(id), 0) FROM actors)));

-- +goose Down
DROP INDEX IF EXISTS actors_workflow_run_unique;
DROP INDEX IF EXISTS actors_bot_unique;
DROP INDEX IF EXISTS actors_agent_session_unique;
DROP INDEX IF EXISTS actors_agent_role_unique;
DROP INDEX IF EXISTS actors_user_unique;
DROP TABLE IF EXISTS actors;
