-- +goose Up
--
-- Add actor_* columns to issues, issue_comments, issue_events,
-- issue_attachments, and contributions tables.
-- This is the persistence side of the unified Actor model.
-- Old columns (author_id, agent_role) are kept for backward compat.

-- issues: add actor columns
ALTER TABLE issues ADD COLUMN actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE issues ADD COLUMN actor_user_id BIGINT;
ALTER TABLE issues ADD COLUMN actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE issues ADD COLUMN actor_workflow_run_id BIGINT;
ALTER TABLE issues ADD COLUMN actor_display_name TEXT NOT NULL DEFAULT '';

-- issue_comments: add actor columns
ALTER TABLE issue_comments ADD COLUMN actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_comments ADD COLUMN actor_user_id BIGINT;
ALTER TABLE issue_comments ADD COLUMN actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_comments ADD COLUMN actor_workflow_run_id BIGINT;
ALTER TABLE issue_comments ADD COLUMN actor_display_name TEXT NOT NULL DEFAULT '';

-- issue_events: add actor columns
ALTER TABLE issue_events ADD COLUMN actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_events ADD COLUMN actor_user_id BIGINT;
ALTER TABLE issue_events ADD COLUMN actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_events ADD COLUMN actor_workflow_run_id BIGINT;
ALTER TABLE issue_events ADD COLUMN actor_display_name TEXT NOT NULL DEFAULT '';

-- issue_attachments: add actor columns
ALTER TABLE issue_attachments ADD COLUMN actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_attachments ADD COLUMN actor_user_id BIGINT;
ALTER TABLE issue_attachments ADD COLUMN actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_attachments ADD COLUMN actor_workflow_run_id BIGINT;
ALTER TABLE issue_attachments ADD COLUMN actor_display_name TEXT NOT NULL DEFAULT '';

-- contributions: add actor columns. Keep agent_role for ref ACL / namespace.
ALTER TABLE contributions ADD COLUMN actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE contributions ADD COLUMN actor_user_id BIGINT;
ALTER TABLE contributions ADD COLUMN actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE contributions ADD COLUMN actor_workflow_run_id BIGINT;
ALTER TABLE contributions ADD COLUMN actor_display_name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE issues DROP COLUMN IF EXISTS actor_kind;
ALTER TABLE issues DROP COLUMN IF EXISTS actor_user_id;
ALTER TABLE issues DROP COLUMN IF EXISTS actor_role_key;
ALTER TABLE issues DROP COLUMN IF EXISTS actor_workflow_run_id;
ALTER TABLE issues DROP COLUMN IF EXISTS actor_display_name;

ALTER TABLE issue_comments DROP COLUMN IF EXISTS actor_kind;
ALTER TABLE issue_comments DROP COLUMN IF EXISTS actor_user_id;
ALTER TABLE issue_comments DROP COLUMN IF EXISTS actor_role_key;
ALTER TABLE issue_comments DROP COLUMN IF EXISTS actor_workflow_run_id;
ALTER TABLE issue_comments DROP COLUMN IF EXISTS actor_display_name;

ALTER TABLE issue_events DROP COLUMN IF EXISTS actor_kind;
ALTER TABLE issue_events DROP COLUMN IF EXISTS actor_user_id;
ALTER TABLE issue_events DROP COLUMN IF EXISTS actor_role_key;
ALTER TABLE issue_events DROP COLUMN IF EXISTS actor_workflow_run_id;
ALTER TABLE issue_events DROP COLUMN IF EXISTS actor_display_name;

ALTER TABLE issue_attachments DROP COLUMN IF EXISTS actor_kind;
ALTER TABLE issue_attachments DROP COLUMN IF EXISTS actor_user_id;
ALTER TABLE issue_attachments DROP COLUMN IF EXISTS actor_role_key;
ALTER TABLE issue_attachments DROP COLUMN IF EXISTS actor_workflow_run_id;
ALTER TABLE issue_attachments DROP COLUMN IF EXISTS actor_display_name;

ALTER TABLE contributions DROP COLUMN IF EXISTS actor_kind;
ALTER TABLE contributions DROP COLUMN IF EXISTS actor_user_id;
ALTER TABLE contributions DROP COLUMN IF EXISTS actor_role_key;
ALTER TABLE contributions DROP COLUMN IF EXISTS actor_workflow_run_id;
ALTER TABLE contributions DROP COLUMN IF EXISTS actor_display_name;
