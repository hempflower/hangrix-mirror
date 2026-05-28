-- +goose Up
--
-- Replace questionnaire_answers.user_id (FK → users, semantic = answered_by)
-- with actor_id (FK → actors).
--
-- IF NOT EXISTS is acceptable here because this is a baseline-style one-time
-- migration within a coordinated multi-module release. The column either
-- exists from this migration or it doesn't; no other module will independently
-- add it. Idempotent on re-run is an operational safety net.
ALTER TABLE questionnaire_answers ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- Backfill: join actors on user_id.
UPDATE questionnaire_answers SET actor_id = a.id
FROM actors a
WHERE a.user_id = questionnaire_answers.user_id
  AND a.kind = 'user'
  AND questionnaire_answers.actor_id IS NULL;

-- System fallback for any remaining NULLs.
UPDATE questionnaire_answers SET actor_id = 1 WHERE actor_id IS NULL;

ALTER TABLE questionnaire_answers ALTER COLUMN actor_id SET NOT NULL;
ALTER TABLE questionnaire_answers ADD FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE RESTRICT;
ALTER TABLE questionnaire_answers DROP COLUMN user_id;

-- +goose Down
ALTER TABLE questionnaire_answers ADD COLUMN IF NOT EXISTS user_id BIGINT;
UPDATE questionnaire_answers SET user_id = a.user_id
FROM actors a
WHERE a.id = questionnaire_answers.actor_id AND a.kind = 'user';
ALTER TABLE questionnaire_answers DROP COLUMN IF EXISTS actor_id;
