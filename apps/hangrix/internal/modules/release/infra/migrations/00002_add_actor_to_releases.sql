-- +goose Up
-- Add actor columns to releases and release_assets.

ALTER TABLE releases ADD COLUMN created_actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN created_actor_user_id BIGINT;
ALTER TABLE releases ADD COLUMN created_actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN created_actor_workflow_run_id BIGINT;
ALTER TABLE releases ADD COLUMN created_actor_display_name TEXT NOT NULL DEFAULT '';

ALTER TABLE releases ADD COLUMN published_actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN published_actor_user_id BIGINT;
ALTER TABLE releases ADD COLUMN published_actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN published_actor_workflow_run_id BIGINT;
ALTER TABLE releases ADD COLUMN published_actor_display_name TEXT NOT NULL DEFAULT '';

ALTER TABLE release_assets ADD COLUMN uploaded_actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE release_assets ADD COLUMN uploaded_actor_user_id BIGINT;
ALTER TABLE release_assets ADD COLUMN uploaded_actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE release_assets ADD COLUMN uploaded_actor_workflow_run_id BIGINT;
ALTER TABLE release_assets ADD COLUMN uploaded_actor_display_name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE releases DROP COLUMN IF EXISTS created_actor_kind;
ALTER TABLE releases DROP COLUMN IF EXISTS created_actor_user_id;
ALTER TABLE releases DROP COLUMN IF EXISTS created_actor_role_key;
ALTER TABLE releases DROP COLUMN IF EXISTS created_actor_workflow_run_id;
ALTER TABLE releases DROP COLUMN IF EXISTS created_actor_display_name;
ALTER TABLE releases DROP COLUMN IF EXISTS published_actor_kind;
ALTER TABLE releases DROP COLUMN IF EXISTS published_actor_user_id;
ALTER TABLE releases DROP COLUMN IF EXISTS published_actor_role_key;
ALTER TABLE releases DROP COLUMN IF EXISTS published_actor_workflow_run_id;
ALTER TABLE releases DROP COLUMN IF EXISTS published_actor_display_name;

ALTER TABLE release_assets DROP COLUMN IF EXISTS uploaded_actor_kind;
ALTER TABLE release_assets DROP COLUMN IF EXISTS uploaded_actor_user_id;
ALTER TABLE release_assets DROP COLUMN IF EXISTS uploaded_actor_role_key;
ALTER TABLE release_assets DROP COLUMN IF EXISTS uploaded_actor_workflow_run_id;
ALTER TABLE release_assets DROP COLUMN IF EXISTS uploaded_actor_display_name;
