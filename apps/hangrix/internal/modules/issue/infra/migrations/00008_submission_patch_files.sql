-- +goose Up
-- Evolve the patch system from a single unified-diff model to a
-- submission + ordered patch file series model.

-- 1. Drop old status CHECK and add new one (stale removed; applying,
--    withdrawn, voided added).
ALTER TABLE issue_patches DROP CONSTRAINT IF EXISTS issue_patches_status_check;
ALTER TABLE issue_patches ADD CONSTRAINT issue_patches_status_check
    CHECK (status IN ('submitted','applying','applied','rejected','superseded','withdrawn','voided'));

-- 2. Create the patch file series table (before dropping patch_text,
--    so we can backfill legacy records).
CREATE TABLE issue_patch_files (
    id             BIGSERIAL PRIMARY KEY,
    submission_id  BIGINT  NOT NULL REFERENCES issue_patches(id) ON DELETE CASCADE,
    seq            INT     NOT NULL,
    file_name      TEXT    NOT NULL,
    patch_text     TEXT    NOT NULL,
    subject        TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_issue_patch_files_submission ON issue_patch_files(submission_id, seq);

-- 3. Backfill legacy single-patch submissions into the new file-series table.
INSERT INTO issue_patch_files (submission_id, seq, file_name, patch_text)
SELECT id, 1, 'legacy.patch', patch_text
FROM issue_patches
WHERE patch_text IS NOT NULL AND patch_text != '';

-- 4. Drop the single patch_text column — individual files now live in
--    issue_patch_files.
ALTER TABLE issue_patches DROP COLUMN IF EXISTS patch_text;

-- 5. Add patch_count (number of files in the series).  Rows that had a
--    legacy patch_text get patch_count = 1; new submissions start at 0.
ALTER TABLE issue_patches ADD COLUMN patch_count INT NOT NULL DEFAULT 0;
UPDATE issue_patches SET patch_count = 1
WHERE id IN (SELECT submission_id FROM issue_patch_files);

-- 6. Add apply_error for recording failures during async workspace apply.
ALTER TABLE issue_patches ADD COLUMN apply_error TEXT NOT NULL DEFAULT '';

-- +goose Down
DROP INDEX IF EXISTS idx_issue_patch_files_submission;
DROP TABLE IF EXISTS issue_patch_files;
ALTER TABLE issue_patches DROP COLUMN IF EXISTS apply_error;
ALTER TABLE issue_patches DROP COLUMN IF EXISTS patch_count;
ALTER TABLE issue_patches ADD COLUMN patch_text TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_patches DROP CONSTRAINT IF EXISTS issue_patches_status_check;
ALTER TABLE issue_patches ADD CONSTRAINT issue_patches_status_check
    CHECK (status IN ('submitted','stale','applied','rejected','superseded'));
