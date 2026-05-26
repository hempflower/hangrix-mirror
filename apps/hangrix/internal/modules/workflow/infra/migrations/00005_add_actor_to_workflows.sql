-- +goose Up
-- Add actor columns to workflow_runs.
-- trigger_actor: who triggered this run (user, agent, workflow, system)
-- run_actor:    the workflow run itself as an actor for downstream side effects

ALTER TABLE workflow_runs ADD COLUMN trigger_actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN trigger_actor_user_id BIGINT;
ALTER TABLE workflow_runs ADD COLUMN trigger_actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN trigger_actor_workflow_run_id BIGINT;
ALTER TABLE workflow_runs ADD COLUMN trigger_actor_display_name TEXT NOT NULL DEFAULT '';

ALTER TABLE workflow_runs ADD COLUMN run_actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN run_actor_user_id BIGINT;
ALTER TABLE workflow_runs ADD COLUMN run_actor_role_key TEXT NOT NULL DEFAULT '';
ALTER TABLE workflow_runs ADD COLUMN run_actor_workflow_run_id BIGINT;
ALTER TABLE workflow_runs ADD COLUMN run_actor_display_name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS trigger_actor_kind;
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS trigger_actor_user_id;
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS trigger_actor_role_key;
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS trigger_actor_workflow_run_id;
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS trigger_actor_display_name;

ALTER TABLE workflow_runs DROP COLUMN IF EXISTS run_actor_kind;
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS run_actor_user_id;
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS run_actor_role_key;
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS run_actor_workflow_run_id;
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS run_actor_display_name;
