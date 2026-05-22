-- +goose Up
-- Evolve the patch system from a single unified-diff model to a
-- submission + ordered patch file series model.

-- 1. Drop old status CHECK and add new one (stale removed; applying,
--    withdrawn, voided added).
ALTER TABLE issue_patches DROP CONSTRAINT IF EXISTS issue_patches_status_check;
ALTER TABLE issue_patches ADD CONSTRAINT issue_patches_status_check
    CHECK (status IN ('submitted','applying','applied','rejected','superseded','withdrawn','voided'));

-- 2. Drop the single patch_text column — individual files live in the
--    new issue_patch_files table.
ALTER TABLE issue_patches DROP COLUMN IF EXISTS patch_text;

-- 3. Add patch_count (number of files in the series).
ALTER TABLE issue_patches ADD COLUMN patch_count INT NOT NULL DEFAULT 0;

-- 4. Add apply_error for recording failures during async workspace apply.
ALTER TABLE issue_patches ADD COLUMN apply_error TEXT NOT NULL DEFAULT '';

-- 5. Create the patch file series table.
CREATE TABLE issue_patch_files (
    id             BIGSERIAL PRIMARY KEY,
    submission_id  BIGINT  NOT NULL REFERENCES issue_patches(id) ON DELETE CASCADE,
    seq            INT     NOT NULL,
    file_name      TEXT    NOT NULL,
    patch_text     TEXT    NOT NULL,
    subject        TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_issue_patch_files_submission ON issue_patch_files(submission_id, seq);

-- +goose Down
DROP INDEX IF EXISTS idx_issue_patch_files_submission;
DROP TABLE IF EXISTS issue_patch_files;
ALTER TABLE issue_patches DROP COLUMN IF EXISTS apply_error;
ALTER TABLE issue_patches DROP COLUMN IF EXISTS patch_count;
ALTER TABLE issue_patches ADD COLUMN patch_text TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_patches DROP CONSTRAINT IF EXISTS issue_patches_status_check;
ALTER TABLE issue_patches ADD CONSTRAINT issue_patches_status_check
    CHECK (status IN ('submitted','stale','applied','rejected','superseded'));
