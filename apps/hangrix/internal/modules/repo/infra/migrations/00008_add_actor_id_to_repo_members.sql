-- +goose Up
--
-- Replace repo_members.added_by (FK → users) with actor_id (FK → actors).
-- repo_members.user_id stays — it is the human member (semantic = human).
-- actor_id tracks who added the member (human user or agent role).
--
-- IF NOT EXISTS is acceptable here because this is a baseline-style one-time
-- migration within a coordinated multi-module release. The column either
-- exists from this migration or it doesn't; no other module will independently
-- add it. Idempotent on re-run is an operational safety net.
ALTER TABLE repo_members ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- Backfill: join actors on user_id for rows where added_by is a human user.
UPDATE repo_members SET actor_id = a.id
FROM actors a
WHERE a.user_id = repo_members.added_by
  AND a.kind = 'user'
  AND repo_members.actor_id IS NULL;

-- System fallback for any remaining NULLs.
UPDATE repo_members SET actor_id = 1 WHERE actor_id IS NULL;

ALTER TABLE repo_members ALTER COLUMN actor_id SET NOT NULL;
ALTER TABLE repo_members ADD FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;
ALTER TABLE repo_members DROP COLUMN added_by;

-- +goose Down
ALTER TABLE repo_members ADD COLUMN IF NOT EXISTS added_by BIGINT;
UPDATE repo_members SET added_by = a.user_id
FROM actors a
WHERE a.id = repo_members.actor_id AND a.kind = 'user';
ALTER TABLE repo_members DROP COLUMN IF EXISTS actor_id;
