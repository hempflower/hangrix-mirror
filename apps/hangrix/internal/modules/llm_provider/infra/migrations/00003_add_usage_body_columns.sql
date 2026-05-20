-- +goose Up
-- request_body captures the raw inbound JSON the proxy received from the
-- caller. response_body captures the raw JSON the upstream returned.
-- Both are TEXT (unbounded) — they are only surfaced on the single-row
-- detail endpoint, never in the list query, so the list stays fast.

ALTER TABLE llm_usage_log
    ADD COLUMN request_body  TEXT NOT NULL DEFAULT '',
    ADD COLUMN response_body TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE llm_usage_log
    DROP COLUMN IF EXISTS request_body,
    DROP COLUMN IF EXISTS response_body;
