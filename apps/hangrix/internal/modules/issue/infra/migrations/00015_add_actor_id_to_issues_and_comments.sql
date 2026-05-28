-- +goose Up
--
-- Phase 3d Batch 3: replace the 6 denormalized actor_* columns (plus legacy
-- author_id + agent_role) on issues and issue_comments with a single
-- actor_id FK → actors(id).
--
-- The backfill resolves each row's actor via the actors table using the
-- denormalized columns as discriminants. Rows that cannot be resolved fall
-- back to the system actor (id=1).
--
-- IF NOT EXISTS / IF EXISTS guards keep the migration idempotent.

-- === issues: add actor_id + backfill ===
ALTER TABLE issues ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- Backfill: user actors (actor_kind='user')
UPDATE issues SET actor_id = a.id
FROM actors a
WHERE a.kind = 'user'
  AND a.user_id = issues.actor_user_id
  AND issues.actor_kind = 'user'
  AND issues.actor_id IS NULL;

-- Backfill: agent actors (actor_kind='agent')
UPDATE issues SET actor_id = a.id
FROM actors a
WHERE a.kind = 'agent_role'
  AND a.agent_role_key = issues.actor_role_key
  AND issues.actor_kind = 'agent'
  AND issues.actor_id IS NULL;

-- Backfill: agent_session actors (actor_kind='agent_session')
UPDATE issues SET actor_id = a.id
FROM actors a
WHERE a.kind = 'agent_session'
  AND a.agent_session_id = issues.actor_workflow_run_id
  AND issues.actor_kind = 'agent_session'
  AND issues.actor_id IS NULL;

-- Backfill: workflow actors (actor_kind='workflow')
UPDATE issues SET actor_id = a.id
FROM actors a
WHERE a.kind = 'workflow_run'
  AND a.workflow_run_id = issues.actor_workflow_run_id
  AND issues.actor_kind = 'workflow'
  AND issues.actor_id IS NULL;

-- Backfill: bot actors (actor_kind='bot')
UPDATE issues SET actor_id = a.id
FROM actors a
WHERE a.kind = 'bot'
  AND a.agent_role_key = issues.actor_role_key
  AND issues.actor_kind = 'bot'
  AND issues.actor_id IS NULL;

-- Backfill: system actor (actor_kind='system' or fallback)
UPDATE issues SET actor_id = 1
WHERE actor_id IS NULL;

-- Enforce NOT NULL + FK
ALTER TABLE issues ALTER COLUMN actor_id SET NOT NULL;
ALTER TABLE issues ADD FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- Drop legacy columns + constraints
ALTER TABLE issues DROP CONSTRAINT IF EXISTS issues_author_xor_agent;
ALTER TABLE issues DROP COLUMN IF EXISTS actor_kind;
ALTER TABLE issues DROP COLUMN IF EXISTS actor_user_id;
ALTER TABLE issues DROP COLUMN IF EXISTS actor_role_key;
ALTER TABLE issues DROP COLUMN IF EXISTS actor_workflow_run_id;
ALTER TABLE issues DROP COLUMN IF EXISTS actor_display_name;
ALTER TABLE issues DROP COLUMN IF EXISTS author_id;
ALTER TABLE issues DROP COLUMN IF EXISTS agent_role;


-- === issue_comments: add actor_id + backfill ===
ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- Backfill: user actors
UPDATE issue_comments SET actor_id = a.id
FROM actors a
WHERE a.kind = 'user'
  AND a.user_id = issue_comments.actor_user_id
  AND issue_comments.actor_kind = 'user'
  AND issue_comments.actor_id IS NULL;

-- Backfill: agent actors
UPDATE issue_comments SET actor_id = a.id
FROM actors a
WHERE a.kind = 'agent_role'
  AND a.agent_role_key = issue_comments.actor_role_key
  AND issue_comments.actor_kind = 'agent'
  AND issue_comments.actor_id IS NULL;

-- Backfill: agent_session actors
UPDATE issue_comments SET actor_id = a.id
FROM actors a
WHERE a.kind = 'agent_session'
  AND a.agent_session_id = issue_comments.actor_workflow_run_id
  AND issue_comments.actor_kind = 'agent_session'
  AND issue_comments.actor_id IS NULL;

-- Backfill: workflow actors
UPDATE issue_comments SET actor_id = a.id
FROM actors a
WHERE a.kind = 'workflow_run'
  AND a.workflow_run_id = issue_comments.actor_workflow_run_id
  AND issue_comments.actor_kind = 'workflow'
  AND issue_comments.actor_id IS NULL;

-- Backfill: bot actors
UPDATE issue_comments SET actor_id = a.id
FROM actors a
WHERE a.kind = 'bot'
  AND a.agent_role_key = issue_comments.actor_role_key
  AND issue_comments.actor_kind = 'bot'
  AND issue_comments.actor_id IS NULL;

-- Backfill: system fallback
UPDATE issue_comments SET actor_id = 1
WHERE actor_id IS NULL;

-- Enforce NOT NULL + FK
ALTER TABLE issue_comments ALTER COLUMN actor_id SET NOT NULL;
ALTER TABLE issue_comments ADD FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- Drop legacy columns + constraints
ALTER TABLE issue_comments DROP CONSTRAINT IF EXISTS issue_comments_author_xor_agent;
ALTER TABLE issue_comments DROP COLUMN IF EXISTS actor_kind;
ALTER TABLE issue_comments DROP COLUMN IF EXISTS actor_user_id;
ALTER TABLE issue_comments DROP COLUMN IF EXISTS actor_role_key;
ALTER TABLE issue_comments DROP COLUMN IF EXISTS actor_workflow_run_id;
ALTER TABLE issue_comments DROP COLUMN IF EXISTS actor_display_name;
ALTER TABLE issue_comments DROP COLUMN IF EXISTS author_id;
ALTER TABLE issue_comments DROP COLUMN IF EXISTS agent_role;


-- +goose Down
-- Restore the legacy columns from the actors table.
ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS actor_user_id BIGINT;
ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS actor_workflow_run_id BIGINT;
ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS actor_display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS author_id BIGINT;
ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS agent_role TEXT NOT NULL DEFAULT '';

UPDATE issue_comments SET
    actor_kind = a.kind,
    actor_user_id = a.user_id,
    actor_role_key = COALESCE(a.agent_role_key, ''),
    actor_workflow_run_id = a.agent_session_id,
    actor_display_name = a.display_name,
    author_id = CASE WHEN a.kind = 'user' THEN a.user_id ELSE NULL END,
    agent_role = CASE WHEN a.kind = 'agent' THEN COALESCE(a.agent_role_key, '') ELSE '' END
FROM actors a
WHERE a.id = issue_comments.actor_id;

ALTER TABLE issue_comments
    ADD CONSTRAINT issue_comments_author_xor_agent CHECK (
        (author_id IS NOT NULL AND agent_role = '') OR
        (author_id IS NULL     AND agent_role <> '')
    );
ALTER TABLE issue_comments DROP COLUMN IF EXISTS actor_id;

ALTER TABLE issues ADD COLUMN IF NOT EXISTS actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE issues ADD COLUMN IF NOT EXISTS actor_user_id BIGINT;
ALTER TABLE issues ADD COLUMN IF NOT EXISTS actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE issues ADD COLUMN IF NOT EXISTS actor_workflow_run_id BIGINT;
ALTER TABLE issues ADD COLUMN IF NOT EXISTS actor_display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE issues ADD COLUMN IF NOT EXISTS author_id BIGINT;
ALTER TABLE issues ADD COLUMN IF NOT EXISTS agent_role TEXT NOT NULL DEFAULT '';

UPDATE issues SET
    actor_kind = a.kind,
    actor_user_id = a.user_id,
    actor_role_key = COALESCE(a.agent_role_key, ''),
    actor_workflow_run_id = a.agent_session_id,
    actor_display_name = a.display_name,
    author_id = CASE WHEN a.kind = 'user' THEN a.user_id ELSE NULL END,
    agent_role = CASE WHEN a.kind = 'agent' THEN COALESCE(a.agent_role_key, '') ELSE '' END
FROM actors a
WHERE a.id = issues.actor_id;

ALTER TABLE issues
    ADD CONSTRAINT issues_author_xor_agent CHECK (
        (author_id IS NOT NULL AND agent_role = '') OR
        (author_id IS NULL     AND agent_role <> '')
    );
ALTER TABLE issues DROP COLUMN IF EXISTS actor_id;
