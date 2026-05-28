-- +goose Up
-- Replace questionnaire_answers.user_id (FK → users) with actor_id (FK → actors).

-- 1. Drop the UNIQUE constraint that includes user_id so we can replace it.
ALTER TABLE questionnaire_answers DROP CONSTRAINT IF EXISTS questionnaire_answers_questionnaire_id_user_id_key;

-- 2. Add the column (nullable initially).
ALTER TABLE questionnaire_answers ADD COLUMN IF NOT EXISTS actor_id BIGINT;

-- 3. Backfill from actors via the old user_id.
UPDATE questionnaire_answers qa
SET actor_id = a.id
FROM actors a
WHERE a.kind = 'user' AND a.user_id = qa.user_id
  AND qa.actor_id IS NULL;

-- 4. Fallback: system actor for any unmapped rows.
UPDATE questionnaire_answers SET actor_id = 1 WHERE actor_id IS NULL;

-- 5. Add FK and make NOT NULL.
ALTER TABLE questionnaire_answers
    ADD CONSTRAINT fk_questionnaire_answers_actor
    FOREIGN KEY (actor_id) REFERENCES actors(id) ON DELETE CASCADE;
ALTER TABLE questionnaire_answers ALTER COLUMN actor_id SET NOT NULL;

-- 6. Drop old column.
ALTER TABLE questionnaire_answers DROP COLUMN user_id;

-- 7. Re-add the UNIQUE constraint with actor_id.
ALTER TABLE questionnaire_answers
    ADD CONSTRAINT questionnaire_answers_questionnaire_id_actor_id_key
    UNIQUE (questionnaire_id, actor_id);

-- +goose Down
ALTER TABLE questionnaire_answers DROP CONSTRAINT IF EXISTS questionnaire_answers_questionnaire_id_actor_id_key;
ALTER TABLE questionnaire_answers ADD COLUMN IF NOT EXISTS user_id BIGINT;
-- Reverse backfill: best-effort from user actors.
UPDATE questionnaire_answers qa
SET user_id = a.user_id
FROM actors a
WHERE a.id = qa.actor_id AND a.kind = 'user'
  AND qa.user_id IS NULL;
-- Fallback to system user.
UPDATE questionnaire_answers SET user_id = 1 WHERE user_id IS NULL;
ALTER TABLE questionnaire_answers ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE questionnaire_answers
    ADD CONSTRAINT fk_questionnaire_answers_user
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE questionnaire_answers DROP CONSTRAINT IF EXISTS fk_questionnaire_answers_actor;
ALTER TABLE questionnaire_answers DROP COLUMN IF EXISTS actor_id;
ALTER TABLE questionnaire_answers
    ADD CONSTRAINT questionnaire_answers_questionnaire_id_user_id_key
    UNIQUE (questionnaire_id, user_id);
