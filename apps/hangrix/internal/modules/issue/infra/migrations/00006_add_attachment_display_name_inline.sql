-- +goose Up
ALTER TABLE issue_attachments
    ADD COLUMN display_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN inline BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE issue_attachments
    DROP COLUMN inline,
    DROP COLUMN display_name;
