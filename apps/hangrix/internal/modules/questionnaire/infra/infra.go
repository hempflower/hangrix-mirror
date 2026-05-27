// Package infra holds the Postgres-backed implementation of the questionnaire
// domain. SQL lives in queries.sql; sqlc generates the typed accessors under
// questionnairedb/.
package infra

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/questionnaire/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/questionnaire/infra/questionnairedb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore implements domain.Store + domain.AnswerStore backed by
// sqlc-generated queries. One PostgresStore satisfies both domain.Store
// and domain.AnswerStore — the module.go binds the same singleton to
// both interfaces (same pattern as issue/domain.PostgresStore).
type PostgresStore struct {
	q    *questionnairedb.Queries
	pool *pgxpool.Pool
}

type PostgresStoreDeps struct {
	Pool *pgxpool.Pool
}

func NewPostgresStore(deps *PostgresStoreDeps) *PostgresStore {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("questionnaire migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_questionnaire", "."); err != nil {
		panic(fmt.Errorf("apply questionnaire migrations: %w", err))
	}
	return &PostgresStore{
		q:    questionnairedb.New(deps.Pool),
		pool: deps.Pool,
	}
}

// ---- Store ---- //

func (s *PostgresStore) Create(ctx context.Context, p domain.CreateParams) (*domain.Questionnaire, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	row, err := qtx.CreateQuestionnaire(ctx, questionnairedb.CreateQuestionnaireParams{
		IssueID:        p.IssueID,
		Title:          p.Title,
		Description:    p.Description,
		CreatedByAgent: p.CreatedByAgent,
	})
	if err != nil {
		return nil, fmt.Errorf("create questionnaire: %w", err)
	}

	qn := questionnaireFromRow(row)

	for _, cq := range p.Questions {
		optsJSON, err := json.Marshal(domainOptionsToJSON(cq.Options))
		if err != nil {
			return nil, fmt.Errorf("marshal options for question %d: %w", cq.Position, err)
		}
		qRow, err := qtx.CreateQuestion(ctx, questionnairedb.CreateQuestionParams{
			QuestionnaireID: row.ID,
			Position:        int32(cq.Position),
			QuestionText:    cq.Text,
			Qtype:           string(cq.Type),
			Options:         optsJSON,
			Required:        cq.Required,
		})
		if err != nil {
			return nil, fmt.Errorf("create question %d: %w", cq.Position, err)
		}
		qn.Questions = append(qn.Questions, questionFromRow(qRow))
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit questionnaire create: %w", err)
	}

	return &qn, nil
}

func (s *PostgresStore) Get(ctx context.Context, id int64) (*domain.Questionnaire, error) {
	row, err := s.q.GetQuestionnaire(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get questionnaire %d: %w", id, err)
	}
	qn := questionnaireFromRow(row)

	questions, err := s.q.GetQuestions(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get questions for %d: %w", id, err)
	}
	for _, q := range questions {
		qn.Questions = append(qn.Questions, questionFromRow(q))
	}

	return &qn, nil
}

func (s *PostgresStore) GetByIssue(ctx context.Context, issueID int64) ([]*domain.Questionnaire, error) {
	rows, err := s.q.GetQuestionnairesByIssue(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("get questionnaires by issue %d: %w", issueID, err)
	}

	var result []*domain.Questionnaire
	for _, row := range rows {
		qn := questionnaireFromRow(row)

		questions, err := s.q.GetQuestions(ctx, row.ID)
		if err != nil {
			return nil, fmt.Errorf("get questions for %d: %w", row.ID, err)
		}
		for _, q := range questions {
			qn.Questions = append(qn.Questions, questionFromRow(q))
		}

		result = append(result, &qn)
	}

	return result, nil
}

func (s *PostgresStore) Close(ctx context.Context, id int64, reason string) (*domain.Questionnaire, error) {
	row, err := s.q.CloseQuestionnaire(ctx, questionnairedb.CloseQuestionnaireParams{
		ID:           id,
		ClosedReason: reason,
	})
	if err != nil {
		return nil, fmt.Errorf("close questionnaire %d: %w", id, err)
	}
	qn := questionnaireFromRow(row)

	questions, err := s.q.GetQuestions(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get questions for %d: %w", id, err)
	}
	for _, q := range questions {
		qn.Questions = append(qn.Questions, questionFromRow(q))
	}

	return &qn, nil
}

// ---- AnswerStore ---- //

func (s *PostgresStore) UpsertAnswer(ctx context.Context, qID, userID int64, perQ map[int64]domain.AnswerValue) (*domain.Answer, error) {
	answersJSON, err := json.Marshal(answerPerQuestionToJSON(perQ))
	if err != nil {
		return nil, fmt.Errorf("marshal answers: %w", err)
	}

	row, err := s.q.UpsertAnswer(ctx, questionnairedb.UpsertAnswerParams{
		QuestionnaireID: qID,
		UserID:          userID,
		Answers:         answersJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("upsert answer: %w", err)
	}

	a := answerFromRow(row)
	return &a, nil
}

func (s *PostgresStore) GetUserAnswer(ctx context.Context, qID, userID int64) (*domain.Answer, error) {
	row, err := s.q.GetUserAnswer(ctx, questionnairedb.GetUserAnswerParams{
		QuestionnaireID: qID,
		UserID:          userID,
	})
	if err != nil {
		return nil, fmt.Errorf("get user answer: %w", err)
	}
	a := answerFromRow(row)
	return &a, nil
}

func (s *PostgresStore) ListAnswers(ctx context.Context, qID int64) ([]*domain.Answer, error) {
	rows, err := s.q.ListAnswers(ctx, qID)
	if err != nil {
		return nil, fmt.Errorf("list answers: %w", err)
	}
	var result []*domain.Answer
	for _, row := range rows {
		a := answerFromRow(row)
		result = append(result, &a)
	}
	return result, nil
}

func (s *PostgresStore) CountAnswers(ctx context.Context, qID int64) (int64, error) {
	cnt, err := s.q.CountAnswers(ctx, qID)
	if err != nil {
		return 0, fmt.Errorf("count answers: %w", err)
	}
	return cnt, nil
}

// ---- Row → domain mappers ---- //

func questionnaireFromRow(row questionnairedb.Questionnaire) domain.Questionnaire {
	qn := domain.Questionnaire{
		ID:             row.ID,
		IssueID:        row.IssueID,
		Title:          row.Title,
		Description:    row.Description,
		Status:         domain.Status(row.Status),
		CreatedByAgent: row.CreatedByAgent,
		CreatedAt:      row.CreatedAt.Time,
		ClosedReason:   row.ClosedReason,
	}
	if row.ClosedAt.Valid {
		t := row.ClosedAt.Time
		qn.ClosedAt = &t
	}
	return qn
}

func questionFromRow(row questionnairedb.QuestionnaireQuestion) domain.Question {
	q := domain.Question{
		ID:       row.ID,
		Position: int(row.Position),
		Text:     row.QuestionText,
		Type:     domain.Qtype(row.Qtype),
		Required: row.Required,
	}
	if len(row.Options) > 0 {
		q.Options = jsonToDomainOptions(row.Options)
	}
	return q
}

func answerFromRow(row questionnairedb.QuestionnaireAnswer) domain.Answer {
	a := domain.Answer{
		ID:              row.ID,
		QuestionnaireID: row.QuestionnaireID,
		UserID:          row.UserID,
		SubmittedAt:     row.SubmittedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
	}
	if len(row.Answers) > 0 {
		a.PerQuestion = jsonToAnswerPerQuestion(row.Answers)
	}
	return a
}

// ---- JSONB conversion helpers ---- //

type jsonOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func domainOptionsToJSON(opts []domain.Option) []jsonOption {
	result := make([]jsonOption, len(opts))
	for i, o := range opts {
		result[i] = jsonOption{ID: o.ID, Label: o.Label}
	}
	return result
}

func jsonToDomainOptions(raw []byte) []domain.Option {
	var js []jsonOption
	if err := json.Unmarshal(raw, &js); err != nil {
		return nil
	}
	result := make([]domain.Option, len(js))
	for i, j := range js {
		result[i] = domain.Option{ID: j.ID, Label: j.Label}
	}
	return result
}

type jsonAnswerValue struct {
	OptionIDs []string `json:"option_ids,omitempty"`
	Text      string   `json:"text,omitempty"`
}

func answerPerQuestionToJSON(perQ map[int64]domain.AnswerValue) map[string]jsonAnswerValue {
	result := make(map[string]jsonAnswerValue, len(perQ))
	for k, v := range perQ {
		result[fmt.Sprintf("%d", k)] = jsonAnswerValue{OptionIDs: v.OptionIDs, Text: v.Text}
	}
	return result
}

func jsonToAnswerPerQuestion(raw []byte) map[int64]domain.AnswerValue {
	var js map[string]jsonAnswerValue
	if err := json.Unmarshal(raw, &js); err != nil {
		return nil
	}
	result := make(map[int64]domain.AnswerValue, len(js))
	for k, v := range js {
		var id int64
		fmt.Sscanf(k, "%d", &id)
		result[id] = domain.AnswerValue{OptionIDs: v.OptionIDs, Text: v.Text}
	}
	return result
}


