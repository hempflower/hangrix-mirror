-- +goose Up

-- disabled flips a provider out of routing without losing the row. Once
-- TRUE, FindProviderByModel skips it (so the proxy returns 404 instead of
-- the disabled upstream), but existing usage_log rows and admin views are
-- untouched. Mirrors the user.disabled convention so the meaning stays
-- uniform across the platform.
ALTER TABLE llm_providers
    ADD COLUMN disabled BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE llm_providers
    DROP COLUMN IF EXISTS disabled;
