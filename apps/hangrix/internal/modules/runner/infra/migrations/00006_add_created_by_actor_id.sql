-- +goose Up
--
-- Add created_by_actor_id to agent_sessions so the spawner can record
-- the resolved actor row from the new actors table. The column starts
-- nullable because legacy rows have no actor; new rows from the spawner
-- are written with a non-null value after the EnsureAgentRole call
-- succeeds. The old created_by FK to users(id) is kept for backward
-- compatibility and will be dropped in a follow-up migration once all
-- callers have migrated to the actor column.
--
-- IF NOT EXISTS is acceptable here because this is a one-time baseline
-- migration; the column either exists from this migration or it doesn't.
ALTER TABLE agent_sessions
ADD COLUMN IF NOT EXISTS created_by_actor_id BIGINT;

-- +goose Down
ALTER TABLE agent_sessions
DROP COLUMN IF EXISTS created_by_actor_id;
