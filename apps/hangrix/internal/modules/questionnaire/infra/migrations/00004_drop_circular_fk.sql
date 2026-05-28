-- +goose Up
-- Drop cross-module hard FKs on questionnaires and questionnaire_answers
-- so that questionnaire migrations can run before the referenced tables
-- (issues, users) exist on a fresh database. AGENTS.md:128 recommends
-- storing cross-module IDs as plain BIGINT and letting the consumer
-- module own the lookup rather than relying on hard FKs.
ALTER TABLE questionnaires      DROP CONSTRAINT IF EXISTS questionnaires_issue_id_fkey;
ALTER TABLE questionnaire_answers DROP CONSTRAINT IF EXISTS questionnaire_answers_user_id_fkey;

-- +goose Down
-- Restore the FKs for rollback. The referenced tables must exist or the
-- re-add will fail — the down direction is only safe when the original
-- constraint names are still in use.
ALTER TABLE questionnaires      ADD FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE;
ALTER TABLE questionnaire_answers ADD FOREIGN KEY (user_id)  REFERENCES users(id)  ON DELETE CASCADE;
