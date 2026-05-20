-- +goose Up
CREATE TABLE releases (
    id BIGSERIAL PRIMARY KEY,
    repo_id BIGINT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    tag_name TEXT NOT NULL,
    target_commit_sha TEXT NOT NULL,
    title TEXT NOT NULL,
    notes TEXT NOT NULL DEFAULT '',
    is_draft BOOLEAN NOT NULL DEFAULT TRUE,
    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(repo_id, tag_name)
);

CREATE INDEX idx_releases_repo_id ON releases(repo_id);

CREATE TABLE release_assets (
    id BIGSERIAL PRIMARY KEY,
    release_id BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0,
    storage_key TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(release_id, name)
);

CREATE INDEX idx_release_assets_release_id ON release_assets(release_id);

-- +goose Down
DROP TABLE IF EXISTS release_assets;
DROP TABLE IF EXISTS releases;
