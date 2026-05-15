-- +goose Up

-- runners are the machines that lease agent containers.
--
-- Two long-lived secrets attach to each row:
--   * enroll token (hgxe_<prefix>_<secret>) — single-use, redeemed at first
--     contact for the agent token. Stored as (prefix, bcrypt(secret)) plus a
--     used_at sentinel so a second redemption attempt 403s.
--   * agent token  (hgxr_<prefix>_<secret>) — long-lived bearer the runner
--     presents on every poll/heartbeat. Same (prefix, bcrypt(secret)) shape
--     so revocation is one UPDATE.
--
-- visibility splits the dispatch pool: 'platform' is admin-owned and
-- schedulable by any session; 'user' is owner-private and schedulable only
-- by sessions belonging to the same user. owner_user_id is NULL precisely
-- when visibility = 'platform'.
CREATE TABLE runners (
    id                    BIGSERIAL PRIMARY KEY,
    name                  TEXT        NOT NULL,
    owner_user_id         BIGINT      REFERENCES users(id) ON DELETE CASCADE,
    visibility            TEXT        NOT NULL CHECK (visibility IN ('platform', 'user')),
    status                TEXT        NOT NULL CHECK (status IN ('pending', 'active', 'disabled')) DEFAULT 'pending',
    capabilities          JSONB       NOT NULL DEFAULT '{}'::jsonb,
    last_heartbeat_at     TIMESTAMPTZ,

    enroll_token_prefix   TEXT        NOT NULL UNIQUE,
    enroll_token_hash     TEXT        NOT NULL,
    enroll_token_used_at  TIMESTAMPTZ,

    agent_token_prefix    TEXT        UNIQUE,
    agent_token_hash      TEXT,
    agent_token_revoked_at TIMESTAMPTZ,

    created_by            BIGINT      NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT runners_visibility_owner CHECK (
        (visibility = 'platform' AND owner_user_id IS NULL) OR
        (visibility = 'user'     AND owner_user_id IS NOT NULL)
    )
);

CREATE INDEX runners_owner_idx     ON runners (owner_user_id) WHERE owner_user_id IS NOT NULL;
CREATE INDEX runners_visibility_idx ON runners (visibility, status);

-- agent_sessions is one row per (admin-triggered or platform-routed) agent
-- run. Every session owns its own identity token (`hgxs_<prefix>_<secret>`)
-- which is what the in-container agent presents to every platform surface
-- (LLM proxy now, MCP server next). The token is NOT coupled to an LLM
-- provider or a specific model — it just identifies the agent.
--
-- Lifecycle:
--   pending   — created, no runner has claimed it yet.
--   claimed   — runner.id has the lease, container is being prepared.
--   running   — agent has reported `status: ready`.
--   succeeded — agent emitted `done` and exited 0.
--   failed    — agent exited non-zero or runner reported error.
--   cancelled — admin-cancelled before runner claimed it.
--
-- Storage rules for the session token columns:
--   session_token_prefix / session_token_hash → bcrypt-style validation row.
--   session_token_sealed                       → cryptobox-sealed plaintext;
--      the runner fetches it once at claim time so the agent's container
--      receives HANGRIX_SESSION_TOKEN. NULL'd at terminate time so a leaked
--      DB dump of a finished session does not carry the bearer.
--   session_token_revoked_at                   → explicit revoke sentinel
--      orthogonal to terminal status (M7a admin revoke surface).
CREATE TABLE agent_sessions (
    id                     BIGSERIAL PRIMARY KEY,
    runner_id              BIGINT      REFERENCES runners(id) ON DELETE SET NULL,
    repo_id                BIGINT      REFERENCES repos(id) ON DELETE SET NULL,
    issue_number           INTEGER,
    status                 TEXT        NOT NULL CHECK (status IN ('pending', 'claimed', 'running', 'succeeded', 'failed', 'cancelled')) DEFAULT 'pending',
    role                   TEXT        NOT NULL DEFAULT '',
    model                  TEXT        NOT NULL DEFAULT '',
    agent_image            TEXT        NOT NULL,
    bundle_dir             TEXT        NOT NULL DEFAULT '',
    working_branch         TEXT        NOT NULL DEFAULT '',
    base_branch            TEXT        NOT NULL DEFAULT '',
    host_addendum          TEXT        NOT NULL DEFAULT '',
    env                    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    session_token_prefix   TEXT        NOT NULL UNIQUE,
    session_token_hash     TEXT        NOT NULL,
    session_token_sealed   TEXT,
    session_token_revoked_at TIMESTAMPTZ,
    exit_code              INTEGER,
    error_message          TEXT        NOT NULL DEFAULT '',
    created_by             BIGINT      NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at             TIMESTAMPTZ,
    started_at             TIMESTAMPTZ,
    ended_at               TIMESTAMPTZ
);

CREATE INDEX agent_sessions_runner_status_idx   ON agent_sessions (runner_id, status);
CREATE INDEX agent_sessions_status_created_idx  ON agent_sessions (status, created_at);

-- agent_session_messages is the flat append-only log per session. Mirrors
-- the IPC kinds the agent emits (status / message / tool_call / log) plus
-- platform-side events (issue.comment.mentioned / commit.pushed / ...)
-- inserted at the moment they fired. seq is monotonically increasing within
-- a session; (session_id, seq) is the natural sort key for replay.
CREATE TABLE agent_session_messages (
    id            BIGSERIAL PRIMARY KEY,
    session_id    BIGINT      NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    seq           INTEGER     NOT NULL,
    kind          TEXT        NOT NULL CHECK (kind IN ('event', 'message', 'tool_call', 'status', 'log', 'done', 'system')),
    role          TEXT        NOT NULL DEFAULT '',
    content       TEXT        NOT NULL DEFAULT '',
    event_name    TEXT        NOT NULL DEFAULT '',
    tool_call_id  TEXT        NOT NULL DEFAULT '',
    tool_name     TEXT        NOT NULL DEFAULT '',
    payload       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (session_id, seq)
);

CREATE INDEX agent_session_messages_session_idx ON agent_session_messages (session_id, seq);

-- agent_session_inputs is the runner-bound queue of inbound IPC frames the
-- platform wants delivered to the agent's stdin. The first frame for any
-- new session is auto-seeded as kind='history' with the replayed message
-- log; subsequent rows come from M7b's event bus (one row per fanout).
-- The runner long-polls this queue, marks consumed_at, and writes the JSON
-- payload directly to the container's stdin pipe.
CREATE TABLE agent_session_inputs (
    id           BIGSERIAL PRIMARY KEY,
    session_id   BIGINT      NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    seq          INTEGER     NOT NULL,
    payload      JSONB       NOT NULL,
    consumed_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (session_id, seq)
);

CREATE INDEX agent_session_inputs_session_idx
    ON agent_session_inputs (session_id, seq)
    WHERE consumed_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS agent_session_inputs_session_idx;
DROP TABLE IF EXISTS agent_session_inputs;

DROP INDEX IF EXISTS agent_session_messages_session_idx;
DROP TABLE IF EXISTS agent_session_messages;

DROP INDEX IF EXISTS agent_sessions_status_created_idx;
DROP INDEX IF EXISTS agent_sessions_runner_status_idx;
DROP TABLE IF EXISTS agent_sessions;

DROP INDEX IF EXISTS runners_visibility_idx;
DROP INDEX IF EXISTS runners_owner_idx;
DROP TABLE IF EXISTS runners;
