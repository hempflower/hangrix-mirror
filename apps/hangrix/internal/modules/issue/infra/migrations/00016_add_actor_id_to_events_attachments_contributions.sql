-- +goose Up
--
-- Phase 3d Batch 4: replace the 5 denormalized actor_* columns (plus legacy
-- author_id/agent_role where present) on issue_events, issue_attachments,
-- and contributions with a single actor_id FK → actors(id).
--
-- The backfill resolves each row's actor via the actors table using the
-- denormalized columns as discriminants. Rows that cannot be resolved fall
-- back to the system actor (id=1).
--
-- IF NOT EXISTS / IF EXISTS guards keep the migration idempotent.


-- === issue_events: repurpose existing actor_id (was FK→users) to FK→actors ===

-- Drop the old FK constraint (it pointed at users, not actors).
ALTER TABLE issue_events DROP CONSTRAINT IF EXISTS issue_events_actor_id_fkey;

-- Clear old user-ID values so the backfill can repopulate with actor IDs.
UPDATE issue_events SET actor_id = NULL;

-- Backfill: user actors (actor_kind='user')
UPDATE issue_events SET actor_id = a.id
FROM actors a
WHERE a.kind = 'user'
  AND a.user_id = issue_events.actor_user_id
  AND issue_events.actor_kind = 'user'
  AND issue_events.actor_id IS NULL;

-- Backfill: agent actors (actor_kind='agent')
UPDATE issue_events SET actor_id = a.id
FROM actors a
WHERE a.kind = 'agent_role'
  AND a.agent_role_key = issue_events.actor_role_key
  AND issue_events.actor_kind = 'agent'
  AND issue_events.actor_id IS NULL;

-- Backfill: agent_session actors (actor_kind='agent_session')
UPDATE issue_events SET actor_id = a.id
FROM actors a
WHERE a.kind = 'agent_session'
  AND a.agent_session_id = issue_events.actor_workflow_run_id
  AND issue_events.actor_kind = 'agent_session'
  AND issue_events.actor_id IS NULL;

-- Backfill: workflow actors (actor_kind='workflow')
UPDATE issue_events SET actor_id = a.id
FROM actors a
WHERE a.kind = 'workflow_run'
  AND a.workflow_run_id = issue_events.actor_workflow_run_id
  AND issue_events.actor_kind = 'workflow'
  AND issue_events.actor_id IS NULL;

-- Backfill: bot actors (actor_kind='bot')
UPDATE issue_events SET actor_id = a.id
FROM actors a
WHERE a.kind = 'bot'
  AND a.agent_role_key = issue_events.actor_role_key
  AND issue_events.actor_kind = 'bot'
  AND issue_events.actor_id IS NULL;

-- Backfill: system fallback
UPDATE issue_events SET actor_id = 1
WHERE actor_id IS NULL;

-- Enforce NOT NULL + FK → actors
ALTER TABLE issue_events ALTER COLUMN actor_id SET NOT NULL;
ALTER TABLE issue_events ADD FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- Drop legacy columns
ALTER TABLE issue_events DROP COLUMN IF EXISTS actor_kind;
ALTER TABLE issue_events DROP COLUMN IF EXISTS actor_user_id;
ALTER TABLE issue_events DROP COLUMN IF EXISTS actor_role_key;
ALTER TABLE issue_events DROP COLUMN IF EXISTS actor_workflow_run_id;
ALTER TABLE issue_events DROP COLUMN IF EXISTS actor_display_name;
ALTER TABLE issue_events DROP COLUMN IF EXISTS agent_role;


-- === issue_attachments: add actor_id + backfill ===
ALTER TABLE issue_attachments ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- Backfill: user actors (author_id is set, agent_role is '')
UPDATE issue_attachments SET actor_id = a.id
FROM actors a
WHERE a.kind = 'user'
  AND a.user_id = issue_attachments.actor_user_id
  AND issue_attachments.actor_kind = 'user'
  AND issue_attachments.actor_id IS NULL;

-- Backfill: agent actors
UPDATE issue_attachments SET actor_id = a.id
FROM actors a
WHERE a.kind = 'agent_role'
  AND a.agent_role_key = issue_attachments.actor_role_key
  AND issue_attachments.actor_kind = 'agent'
  AND issue_attachments.actor_id IS NULL;

-- Backfill: agent_session actors
UPDATE issue_attachments SET actor_id = a.id
FROM actors a
WHERE a.kind = 'agent_session'
  AND a.agent_session_id = issue_attachments.actor_workflow_run_id
  AND issue_attachments.actor_kind = 'agent_session'
  AND issue_attachments.actor_id IS NULL;

-- Backfill: workflow actors
UPDATE issue_attachments SET actor_id = a.id
FROM actors a
WHERE a.kind = 'workflow_run'
  AND a.workflow_run_id = issue_attachments.actor_workflow_run_id
  AND issue_attachments.actor_kind = 'workflow'
  AND issue_attachments.actor_id IS NULL;

-- Backfill: bot actors
UPDATE issue_attachments SET actor_id = a.id
FROM actors a
WHERE a.kind = 'bot'
  AND a.agent_role_key = issue_attachments.actor_role_key
  AND issue_attachments.actor_kind = 'bot'
  AND issue_attachments.actor_id IS NULL;

-- Backfill: system fallback
UPDATE issue_attachments SET actor_id = 1
WHERE actor_id IS NULL;

-- Enforce NOT NULL + FK
ALTER TABLE issue_attachments ALTER COLUMN actor_id SET NOT NULL;
ALTER TABLE issue_attachments ADD FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- Drop legacy columns + constraints
ALTER TABLE issue_attachments DROP CONSTRAINT IF EXISTS issue_attachments_author_xor_agent;
ALTER TABLE issue_attachments DROP COLUMN IF EXISTS actor_kind;
ALTER TABLE issue_attachments DROP COLUMN IF EXISTS actor_user_id;
ALTER TABLE issue_attachments DROP COLUMN IF EXISTS actor_role_key;
ALTER TABLE issue_attachments DROP COLUMN IF EXISTS actor_workflow_run_id;
ALTER TABLE issue_attachments DROP COLUMN IF EXISTS actor_display_name;
ALTER TABLE issue_attachments DROP COLUMN IF EXISTS author_id;
ALTER TABLE issue_attachments DROP COLUMN IF EXISTS agent_role;


-- === contributions: add actor_id + backfill ===
ALTER TABLE contributions ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- Backfill: agent actors (contributions are always agent-authored)
UPDATE contributions SET actor_id = a.id
FROM actors a
WHERE a.kind = 'agent_role'
  AND a.agent_role_key = contributions.actor_role_key
  AND contributions.actor_kind = 'agent'
  AND contributions.actor_id IS NULL;

-- Backfill: agent_session actors
UPDATE contributions SET actor_id = a.id
FROM actors a
WHERE a.kind = 'agent_session'
  AND a.agent_session_id = contributions.actor_workflow_run_id
  AND contributions.actor_kind = 'agent_session'
  AND contributions.actor_id IS NULL;

-- Backfill: workflow actors
UPDATE contributions SET actor_id = a.id
FROM actors a
WHERE a.kind = 'workflow_run'
  AND a.workflow_run_id = contributions.actor_workflow_run_id
  AND contributions.actor_kind = 'workflow'
  AND contributions.actor_id IS NULL;

-- Backfill: bot actors
UPDATE contributions SET actor_id = a.id
FROM actors a
WHERE a.kind = 'bot'
  AND a.agent_role_key = contributions.actor_role_key
  AND contributions.actor_kind = 'bot'
  AND contributions.actor_id IS NULL;

-- Backfill: system fallback (unlikely for contributions, but safe)
UPDATE contributions SET actor_id = 1
WHERE actor_id IS NULL;

-- Enforce NOT NULL + FK
ALTER TABLE contributions ALTER COLUMN actor_id SET NOT NULL;
ALTER TABLE contributions ADD FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;

-- Drop denormalized actor columns (keep agent_role for branch namespace / ref ACL)
ALTER TABLE contributions DROP COLUMN IF EXISTS actor_kind;
ALTER TABLE contributions DROP COLUMN IF EXISTS actor_user_id;
ALTER TABLE contributions DROP COLUMN IF EXISTS actor_role_key;
ALTER TABLE contributions DROP COLUMN IF EXISTS actor_workflow_run_id;
ALTER TABLE contributions DROP COLUMN IF EXISTS actor_display_name;


-- +goose Down
-- Restore the legacy columns from the actors table.

-- === contributions: restore actor_* columns ===
ALTER TABLE contributions ADD COLUMN IF NOT EXISTS actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE contributions ADD COLUMN IF NOT EXISTS actor_user_id BIGINT;
ALTER TABLE contributions ADD COLUMN IF NOT EXISTS actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE contributions ADD COLUMN IF NOT EXISTS actor_workflow_run_id BIGINT;
ALTER TABLE contributions ADD COLUMN IF NOT EXISTS actor_display_name TEXT NOT NULL DEFAULT '';

UPDATE contributions SET
    actor_kind = a.kind,
    actor_user_id = a.user_id,
    actor_role_key = COALESCE(a.agent_role_key, ''),
    actor_workflow_run_id = a.agent_session_id,
    actor_display_name = a.display_name
FROM actors a
WHERE a.id = contributions.actor_id;

ALTER TABLE contributions DROP COLUMN IF EXISTS actor_id;


-- === issue_attachments: restore author_id, agent_role, and actor_* columns ===
ALTER TABLE issue_attachments ADD COLUMN IF NOT EXISTS actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_attachments ADD COLUMN IF NOT EXISTS actor_user_id BIGINT;
ALTER TABLE issue_attachments ADD COLUMN IF NOT EXISTS actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_attachments ADD COLUMN IF NOT EXISTS actor_workflow_run_id BIGINT;
ALTER TABLE issue_attachments ADD COLUMN IF NOT EXISTS actor_display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_attachments ADD COLUMN IF NOT EXISTS author_id BIGINT;
ALTER TABLE issue_attachments ADD COLUMN IF NOT EXISTS agent_role TEXT NOT NULL DEFAULT '';

UPDATE issue_attachments SET
    actor_kind = a.kind,
    actor_user_id = a.user_id,
    actor_role_key = COALESCE(a.agent_role_key, ''),
    actor_workflow_run_id = a.agent_session_id,
    actor_display_name = a.display_name,
    author_id = CASE WHEN a.kind = 'user' THEN a.user_id ELSE NULL END,
    agent_role = CASE WHEN a.kind = 'agent_role' THEN COALESCE(a.agent_role_key, '') ELSE '' END
FROM actors a
WHERE a.id = issue_attachments.actor_id;

ALTER TABLE issue_attachments
    ADD CONSTRAINT issue_attachments_author_xor_agent CHECK (
        (author_id IS NOT NULL AND agent_role = '') OR
        (author_id IS NULL     AND agent_role <> '')
    );
ALTER TABLE issue_attachments DROP COLUMN IF EXISTS actor_id;


-- === issue_events: restore agent_role and actor_* columns, old FK → users ===
ALTER TABLE issue_events ADD COLUMN IF NOT EXISTS actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_events ADD COLUMN IF NOT EXISTS actor_user_id BIGINT;
ALTER TABLE issue_events ADD COLUMN IF NOT EXISTS actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_events ADD COLUMN IF NOT EXISTS actor_workflow_run_id BIGINT;
ALTER TABLE issue_events ADD COLUMN IF NOT EXISTS actor_display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_events ADD COLUMN IF NOT EXISTS agent_role TEXT NOT NULL DEFAULT '';

UPDATE issue_events SET
    actor_kind = a.kind,
    actor_user_id = a.user_id,
    actor_role_key = COALESCE(a.agent_role_key, ''),
    actor_workflow_run_id = a.agent_session_id,
    actor_display_name = a.display_name,
    agent_role = CASE WHEN a.kind = 'agent_role' THEN COALESCE(a.agent_role_key, '') ELSE '' END
FROM actors a
WHERE a.id = issue_events.actor_id;

-- Restore the old FK to users. We can't easily reconstruct the old user-ID
-- values for non-user actors, so actor_id becomes NULL for those rows.
UPDATE issue_events SET actor_id = CASE
    WHEN actor_kind = 'user' THEN actor_user_id
    ELSE NULL
END;

ALTER TABLE issue_events ADD FOREIGN KEY (actor_id) REFERENCES users(id) ON DELETE SET NULL;
