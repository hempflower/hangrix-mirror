-- +goose Up
-- Backfill existing open questionnaires that already have answers.
-- After this migration, the invariant "open ⇒ 0 answers" holds so
-- the API contract has no historical exceptions. Idempotent by
-- construction — re-running leaves already-closed rows untouched.
UPDATE questionnaires
SET status = 'closed', closed_at = now(), closed_reason = 'auto:backfill'
WHERE status = 'open'
  AND EXISTS (SELECT 1 FROM questionnaire_answers WHERE questionnaire_id = questionnaires.id);

-- +goose Down
-- No-op: there is no safe way to revert which rows were backfilled.
-- The data is already consistent; rolling forward is the only option.
SELECT 1;
