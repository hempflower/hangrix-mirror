-- +goose Up
CREATE TABLE organizations (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT        NOT NULL UNIQUE,
    display_name TEXT        NOT NULL DEFAULT '',
    description  TEXT        NOT NULL DEFAULT '',
    avatar_url   TEXT        NOT NULL DEFAULT '',
    visibility   TEXT        NOT NULL CHECK (visibility IN ('public', 'private')) DEFAULT 'public',
    created_by   BIGINT      NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at   TIMESTAMPTZ NULL
);

CREATE TABLE organization_members (
    org_id    BIGINT      NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id   BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role      TEXT        NOT NULL CHECK (role IN ('owner', 'member')) DEFAULT 'member',
    added_by  BIGINT      NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    added_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, user_id)
);

CREATE INDEX idx_organization_members_user ON organization_members(user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_organization_members_user;
DROP TABLE organization_members;
DROP TABLE organizations;
