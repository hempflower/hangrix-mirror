-- +goose Up
CREATE TABLE issue_dependencies (
  id            BIGSERIAL PRIMARY KEY,
  repo_id       BIGINT NOT NULL,
  issue_id      BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  depends_on_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  created_by    BIGINT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT issue_deps_no_self CHECK (issue_id <> depends_on_id),
  UNIQUE (issue_id, depends_on_id)
);
CREATE INDEX idx_issue_deps_issue      ON issue_dependencies(issue_id);
CREATE INDEX idx_issue_deps_depends_on ON issue_dependencies(depends_on_id);

-- +goose Down
DROP TABLE issue_dependencies;
