-- +goose Up
-- M5 originally shipped with a `visibility` column on organizations. We
-- dropped the feature: organizations are always visible to any
-- authenticated user. The column goes away here so future joins and DTOs
-- don't have to keep an unused field alive.
ALTER TABLE organizations DROP COLUMN visibility;

-- +goose Down
ALTER TABLE organizations
    ADD COLUMN visibility TEXT NOT NULL
        CHECK (visibility IN ('public', 'private'))
        DEFAULT 'public';
