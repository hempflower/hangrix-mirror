-- +goose Up
-- Drop cross-module FK constraints to break circular dependencies.
--
-- The original 00001 had two cross-module FKs:
--   • questionnaires.issue_id → issues(id)
--   • questionnaire_answers.user_id → users(id)
--
-- These are safe to drop because:
--   • Application-layer validation ensures issue_id validity.
--   • Migration 00003 replaces user_id with actor_id (FK → actors).
-- Module-internal FKs (questionnaire_questions.questionnaire_id →
-- questionnaires.id) are left intact.
ALTER TABLE questionnaires DROP CONSTRAINT IF EXISTS questionnaires_issue_id_fkey;
ALTER TABLE questionnaire_answers DROP CONSTRAINT IF EXISTS questionnaire_answers_user_id_fkey;

-- +goose Down
-- Restore the FK constraints. Only succeeds if all rows reference valid
-- targets.
ALTER TABLE questionnaires ADD FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE;
ALTER TABLE questionnaire_answers ADD FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
