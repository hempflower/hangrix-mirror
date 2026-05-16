-- +goose Up

-- M7a renames agent_sessions.bundle_dir to agent_repo and redefines its
-- semantics. Old meaning: a runner-side filesystem path the admin handed
-- the runner. New meaning: an `<owner>/<name>@<sha>` triple identifying the
-- agent repository content the runner should materialise. The bundle is
-- now resolved on the runner via content-addressed cache against
-- /api/runner/agent-bundles/{owner}/{name}/{sha}.tar.gz; no caller
-- supplies a filesystem path. M6c never reached external users, so we drop
-- the old semantics in place instead of carrying a compat shim.
ALTER TABLE agent_sessions RENAME COLUMN bundle_dir TO agent_repo;

-- M7a snapshot columns. Frozen at session-spawn time so audit can later
-- reconstruct exactly which agent prompt + tool set + host config produced
-- a given commit. The spec lives in docs/agent-config.md ("Session 模型 →
-- 冻结点 = session spawn 那一刻"):
--
--   agent_sha   — commit on the agent repo the runner pulled (resolved from
--                 the @<ref> in host yaml). Snapshotted: ref rotations on
--                 the agent repo do not affect a running session.
--   repo_sha    — commit on the host repo's base branch at spawn time, so
--                 the exact .hangrix/agents.yml + prompt files in effect
--                 for this session can be re-checked-out.
--   role_key    — role identifier from host yaml (e.g. "backend"). Also
--                 the commit author name for any push this session makes.
--   cause_kind  — what kicked off the session: 'issue_opened' |
--                 'comment_mentioned' | 'commit_pushed' | 'review_vote' |
--                 'manual'. Free-form rather than enum so M7b can extend.
--   cause_id    — id of the upstream artefact (comment id, commit sha,
--                 vote id, …). Opaque to the runner module; consumer-typed.
--   role_config — the resolved role config snapshot as JSON: { prompt,
--                 can, llm, container, … }. Lets the audit consumer
--                 reconstruct what the agent saw without re-parsing host
--                 yaml at a later sha.
--
-- The four scalar columns are NULL-tolerant because M6c-era admin sessions
-- (created via /api/admin/runners/{id}/sessions before this migration) do
-- not have snapshot data. New M7a code MUST populate all five.
ALTER TABLE agent_sessions
    ADD COLUMN agent_sha   TEXT NOT NULL DEFAULT '',
    ADD COLUMN repo_sha    TEXT NOT NULL DEFAULT '',
    ADD COLUMN role_key    TEXT NOT NULL DEFAULT '',
    ADD COLUMN cause_kind  TEXT NOT NULL DEFAULT '',
    ADD COLUMN cause_id    TEXT NOT NULL DEFAULT '',
    ADD COLUMN role_config JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX agent_sessions_repo_role_idx
    ON agent_sessions (repo_id, role_key)
    WHERE repo_id IS NOT NULL;

-- M7a state-machine extension. Existing states (pending, claimed, running,
-- succeeded, failed, cancelled) stay in place — they describe a single
-- container lifecycle and the runner pollTasks / terminate path depends on
-- them. M7a adds two more:
--
--   idle     — a per-role session that has finished one turn but the issue
--              is still open. Future triggers (new mention, new push) will
--              rehydrate the same row. The container has been torn down;
--              spinning it back up means a fresh `pending → claimed →
--              running` cycle on this row's id, not creating a new row.
--   archived — the parent issue closed / merged. Container is gone for
--              good. Distinct from succeeded/failed/cancelled because the
--              session itself made no judgement — the issue did.
--
-- Adding values to a CHECK constraint requires drop + recreate; we re-use
-- the original list verbatim and append.
ALTER TABLE agent_sessions DROP CONSTRAINT agent_sessions_status_check;
ALTER TABLE agent_sessions
    ADD CONSTRAINT agent_sessions_status_check
    CHECK (status IN ('pending', 'claimed', 'running', 'succeeded', 'failed', 'cancelled', 'idle', 'archived'));

-- +goose Down

ALTER TABLE agent_sessions DROP CONSTRAINT agent_sessions_status_check;
ALTER TABLE agent_sessions
    ADD CONSTRAINT agent_sessions_status_check
    CHECK (status IN ('pending', 'claimed', 'running', 'succeeded', 'failed', 'cancelled'));

DROP INDEX IF EXISTS agent_sessions_repo_role_idx;

ALTER TABLE agent_sessions
    DROP COLUMN role_config,
    DROP COLUMN cause_id,
    DROP COLUMN cause_kind,
    DROP COLUMN role_key,
    DROP COLUMN repo_sha,
    DROP COLUMN agent_sha;

ALTER TABLE agent_sessions RENAME COLUMN agent_repo TO bundle_dir;
