-- +goose Up
-- +goose StatementBegin
CREATE TABLE questionnaires (
    id               BIGSERIAL    PRIMARY KEY,
    issue_id         BIGINT       NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    title            TEXT         NOT NULL,
    description      TEXT         NOT NULL DEFAULT '',
    status           TEXT         NOT NULL DEFAULT 'open'
                                   CHECK (status IN ('open','closed')),
    created_by_agent TEXT         NOT NULL,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    closed_at        TIMESTAMPTZ,
    closed_reason    TEXT         NOT NULL DEFAULT ''
);
CREATE INDEX questionnaires_issue_idx ON questionnaires(issue_id);

CREATE TABLE questionnaire_questions (
    id               BIGSERIAL PRIMARY KEY,
    questionnaire_id BIGINT    NOT NULL REFERENCES questionnaires(id) ON DELETE CASCADE,
    position         INT       NOT NULL,
    question_text    TEXT      NOT NULL,
    qtype            TEXT      NOT NULL CHECK (qtype IN ('single_choice','multi_choice','text_input')),
    options          JSONB,
    required         BOOLEAN   NOT NULL DEFAULT TRUE,
    UNIQUE (questionnaire_id, position)
);

CREATE TABLE questionnaire_answers (
    id               BIGSERIAL    PRIMARY KEY,
    questionnaire_id BIGINT       NOT NULL REFERENCES questionnaires(id) ON DELETE CASCADE,
    user_id          BIGINT       NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    answers          JSONB        NOT NULL,
    submitted_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (questionnaire_id, user_id)
);
CREATE INDEX questionnaire_answers_q_idx ON questionnaire_answers(questionnaire_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS questionnaire_answers;
DROP TABLE IF EXISTS questionnaire_questions;
DROP TABLE IF EXISTS questionnaires;
-- +goose StatementEnd
