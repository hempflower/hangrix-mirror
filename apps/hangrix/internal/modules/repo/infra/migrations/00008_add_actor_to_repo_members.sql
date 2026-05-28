-- +goose Up
-- Add actor_id to repo_members for future agent-member support.
-- user_id stays as the human-member path (semantic = human);
-- actor_id is the agent path (semantic = agent_role actor).
-- IF NOT EXISTS is acceptable: this is a one-time column addition for a new
-- feature path; the column either exists or it doesn't after this runs.

ALTER TABLE repo_members ADD COLUMN IF NOT EXISTS actor_id BIGINT;

ALTER TABLE repo_members
    ADD CONSTRAINT fk_repo_members_actor
    FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE repo_members DROP CONSTRAINT IF EXISTS fk_repo_members_actor;
ALTER TABLE repo_members DROP COLUMN IF EXISTS actor_id;
