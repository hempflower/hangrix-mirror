-- +goose Up
-- Repair the contributions.status CHECK constraint + default.
--
-- Migration 00009 was edited in place AFTER it had already been applied on
-- some databases: the status vocabulary changed from
--   ('open','changes_requested','merged','closed')   default 'open'
-- to
--   ('pending','approved','rejected','merged','closed') default 'pending'.
-- goose tracks migrations by version and never re-runs an applied one, so
-- those databases kept the OLD constraint and now reject inserts of the new
-- 'pending' status with SQLSTATE 23514 — a pushed contribution branch is
-- recognised but its row can't be written, so it is never recorded.
--
-- This migration converges every database onto the new vocabulary. It is safe
-- on databases that already have the new constraint: the data UPDATEs are
-- no-ops and the constraint is dropped and re-added identically.
ALTER TABLE contributions DROP CONSTRAINT IF EXISTS contributions_status_check;

-- Map any legacy values to the new vocabulary before re-asserting the check.
UPDATE contributions SET status = 'pending'  WHERE status = 'open';
UPDATE contributions SET status = 'rejected' WHERE status = 'changes_requested';

ALTER TABLE contributions ALTER COLUMN status SET DEFAULT 'pending';

ALTER TABLE contributions
    ADD CONSTRAINT contributions_status_check
    CHECK (status IN ('pending','approved','rejected','merged','closed'));

-- +goose Down
-- Best-effort restore of the original vocabulary. 'approved' has no legacy
-- equivalent and is mapped back to the pre-review 'open' state.
ALTER TABLE contributions DROP CONSTRAINT IF EXISTS contributions_status_check;

UPDATE contributions SET status = 'open'              WHERE status IN ('pending','approved');
UPDATE contributions SET status = 'changes_requested' WHERE status = 'rejected';

ALTER TABLE contributions ALTER COLUMN status SET DEFAULT 'open';

ALTER TABLE contributions
    ADD CONSTRAINT contributions_status_check
    CHECK (status IN ('open','changes_requested','merged','closed'));
