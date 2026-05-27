-- +goose Up
--
-- actors is the canonical table for every actor identity in the platform.
-- One row per distinct (kind, discriminant) tuple; ensured by partial
-- unique indexes below. The IF NOT EXISTS guards are acceptable here
-- because this is a baseline-style one-time migration: the table either
-- exists from this migration or it doesn't; cross-module coordination
-- means no other module will try to CREATE TABLE actors independently.
-- Idempotent on re-run is an operational safety net, not an architectural
-- pattern.

CREATE TABLE IF NOT EXISTS actors (
    id                BIGSERIAL    PRIMARY KEY,
    kind              TEXT         NOT NULL CHECK (kind IN ('user', 'agent_role', 'agent_session', 'bot', 'workflow_run', 'system')),
    user_id           BIGINT,
    agent_role_key    TEXT,
    agent_session_id  BIGINT,
    workflow_run_id   BIGINT,
    display_name      TEXT         NOT NULL,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- One actor per user: user_id is unique across the 'user' kind.
CREATE UNIQUE INDEX IF NOT EXISTS actors_user_idx
    ON actors (user_id) WHERE kind = 'user';

-- One actor per agent role key.
CREATE UNIQUE INDEX IF NOT EXISTS actors_agent_role_idx
    ON actors (agent_role_key) WHERE kind = 'agent_role';

-- One actor per agent session.
CREATE UNIQUE INDEX IF NOT EXISTS actors_agent_session_idx
    ON actors (agent_session_id) WHERE kind = 'agent_session';

-- One actor per workflow run.
CREATE UNIQUE INDEX IF NOT EXISTS actors_workflow_run_idx
    ON actors (workflow_run_id) WHERE kind = 'workflow_run';

-- One actor per bot name.
CREATE UNIQUE INDEX IF NOT EXISTS actors_bot_idx
    ON actors (agent_role_key) WHERE kind = 'bot';

-- Lookup by (kind, discriminant) is the hot path for all Ensure* calls.
CREATE INDEX IF NOT EXISTS actors_kind_user_id_idx ON actors (kind, user_id);
CREATE INDEX IF NOT EXISTS actors_kind_agent_role_idx ON actors (kind, agent_role_key);
CREATE INDEX IF NOT EXISTS actors_kind_agent_session_idx ON actors (kind, agent_session_id);
CREATE INDEX IF NOT EXISTS actors_kind_workflow_run_idx ON actors (kind, workflow_run_id);

-- System seed: actor id=1 is always the platform System actor.
-- ON CONFLICT DO NOTHING keeps this idempotent across re-runs.
INSERT INTO actors (id, kind, display_name)
VALUES (1, 'system', 'System')
ON CONFLICT DO NOTHING;

-- +goose Down
DROP INDEX IF EXISTS actors_kind_workflow_run_idx;
DROP INDEX IF EXISTS actors_kind_agent_session_idx;
DROP INDEX IF EXISTS actors_kind_agent_role_idx;
DROP INDEX IF EXISTS actors_kind_user_id_idx;
DROP INDEX IF EXISTS actors_bot_idx;
DROP INDEX IF EXISTS actors_workflow_run_idx;
DROP INDEX IF EXISTS actors_agent_session_idx;
DROP INDEX IF EXISTS actors_agent_role_idx;
DROP INDEX IF EXISTS actors_user_idx;
DROP TABLE IF EXISTS actors;
