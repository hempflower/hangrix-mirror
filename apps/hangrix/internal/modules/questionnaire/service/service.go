// Package service implements the questionnaire business logic:
// option ID generation, answer validation, and result aggregation.
// It wraps the domain.Store + domain.AnswerStore and adds BuildResult.
package service

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/questionnaire/domain"
)

// Service implements domain.Service.
type Service struct {
	store       domain.Store
	answerStore domain.AnswerStore
}

type ServiceDeps struct {
	Store       domain.Store
	AnswerStore domain.AnswerStore
}

func NewService(deps *ServiceDeps) *Service {
	return &Service{
		store:       deps.Store,
		answerStore: deps.AnswerStore,
	}
}

// ---- Store delegation ---- //

func (s *Service) Create(ctx context.Context, p domain.CreateParams) (*domain.Questionnaire, error) {
	// Validate input.
	if errs := p.Validate(); len(errs) > 0 {
		return nil, &ValidationError{Errors: errs}
	}

	// Assign server-generated option IDs.
	for i := range p.Questions {
		q := &p.Questions[i]
		if q.Type == domain.QtypeSingleChoice || q.Type == domain.QtypeMultiChoice {
			for j := range q.Options {
				q.Options[j].ID = generateOptionID()
			}
		}
	}

	return s.store.Create(ctx, p)
}

func (s *Service) Get(ctx context.Context, id int64) (*domain.Questionnaire, error) {
	return s.store.Get(ctx, id)
}

func (s *Service) GetByIssue(ctx context.Context, issueID int64) ([]*domain.Questionnaire, error) {
	return s.store.GetByIssue(ctx, issueID)
}

func (s *Service) Close(ctx context.Context, id int64, reason string) (*domain.Questionnaire, error) {
	// Idempotency guard: if already closed, skip the UPDATE and return
	// the row unchanged to avoid rewriting closed_at.
	qn, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if qn.Status != domain.StatusOpen {
		return qn, nil
	}
	return s.store.Close(ctx, id, reason)
}

// ---- AnswerStore delegation ---- //

func (s *Service) UpsertAnswer(ctx context.Context, qID, userID int64, perQ map[int64]domain.AnswerValue) (*domain.Answer, *domain.Questionnaire, error) {
	// Load questionnaire for fast-path status check and answer validation.
	qn, err := s.store.Get(ctx, qID)
	if err != nil {
		return nil, nil, fmt.Errorf("load questionnaire for validation: %w", err)
	}

	// Fast-path: reject immediately if already closed. Correctness is
	// held by the infra-layer SELECT FOR UPDATE; this is a UX optimisation.
	if qn.Status != domain.StatusOpen {
		return nil, nil, domain.ErrQuestionnaireLocked
	}

	if errs := domain.ValidateAnswer(qn.Questions, perQ); len(errs) > 0 {
		return nil, nil, &ValidationError{Errors: errs}
	}

	answer, closedQn, err := s.answerStore.InsertFirstAnswer(ctx, qID, userID, perQ)
	if err != nil {
		if errors.Is(err, domain.ErrQuestionnaireLocked) {
			return nil, nil, err
		}
		return nil, nil, err
	}

	return answer, closedQn, nil
}

func (s *Service) GetUserAnswer(ctx context.Context, qID, userID int64) (*domain.Answer, error) {
	return s.answerStore.GetUserAnswer(ctx, qID, userID)
}

func (s *Service) ListAnswers(ctx context.Context, qID int64) ([]*domain.Answer, error) {
	return s.answerStore.ListAnswers(ctx, qID)
}

func (s *Service) CountAnswers(ctx context.Context, qID int64) (int64, error) {
	return s.answerStore.CountAnswers(ctx, qID)
}

// ---- BuildResult ---- //

func (s *Service) BuildResult(ctx context.Context, qID int64) (*domain.Result, error) {
	qn, err := s.store.Get(ctx, qID)
	if err != nil {
		return nil, err
	}

	answers, err := s.answerStore.ListAnswers(ctx, qID)
	if err != nil {
		return nil, err
	}

	result := &domain.Result{
		Questionnaire: qn,
		Submissions:   len(answers),
		ByQuestion:    make(map[int64]domain.QuestionResult),
	}

	// Build question result per question.
	for _, q := range qn.Questions {
		qr := domain.QuestionResult{Type: q.Type}

		switch q.Type {
		case domain.QtypeSingleChoice, domain.QtypeMultiChoice:
			tallies := buildChoiceTallies(q, answers)
			qr.Tallies = tallies

		case domain.QtypeTextInput:
			qr.Responses = buildTextResponses(q, answers)
		}

		result.ByQuestion[q.ID] = qr
	}

	// Build submitters list.
	for _, a := range answers {
		sd := domain.SubmitterDetail{
			UserID:      a.UserID,
			SubmittedAt: a.SubmittedAt,
		}
		for _, q := range qn.Questions {
			av, ok := a.PerQuestion[q.ID]
			if !ok {
				continue
			}
			sd.Answers = append(sd.Answers, domain.SubmitterAnswer{
				QuestionID: q.ID,
				OptionIDs:  av.OptionIDs,
				Text:       av.Text,
			})
		}
		result.Submitters = append(result.Submitters, sd)
	}

	return result, nil
}

func buildChoiceTallies(q domain.Question, answers []*domain.Answer) []domain.ChoiceTally {
	// Count per option.
	counts := make(map[string]int)
	for _, a := range answers {
		av, ok := a.PerQuestion[q.ID]
		if !ok {
			continue
		}
		for _, oid := range av.OptionIDs {
			counts[oid]++
		}
	}

	total := len(answers)
	var tallies []domain.ChoiceTally
	for _, o := range q.Options {
		c := counts[o.ID]
		pct := 0.0
		if total > 0 {
			pct = float64(c) / float64(total) * 100
		}
		tallies = append(tallies, domain.ChoiceTally{
			OptionID: o.ID,
			Label:    o.Label,
			Count:    c,
			Percent:  pct,
		})
	}
	return tallies
}

func buildTextResponses(q domain.Question, answers []*domain.Answer) []domain.TextResponse {
	var responses []domain.TextResponse
	for _, a := range answers {
		av, ok := a.PerQuestion[q.ID]
		if !ok || av.Text == "" {
			continue
		}
		responses = append(responses, domain.TextResponse{
			UserID:      a.UserID,
			Text:        av.Text,
			SubmittedAt: a.SubmittedAt,
		})
	}
	return responses
}

// ---- Option ID generation ---- //

var optionIDEncoding = base32.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567").WithPadding(base32.NoPadding)

func generateOptionID() string {
	b := make([]byte, 5) // 5 bytes → 8 base32 chars
	_, _ = rand.Read(b)
	return strings.ToLower(optionIDEncoding.EncodeToString(b))
}

// ---- Error types ---- //

// ValidationError wraps domain-level field errors for service callers.
type ValidationError struct {
	Errors []domain.FieldError
}

func (e *ValidationError) Error() string {
	if len(e.Errors) == 0 {
		return "validation failed"
	}
	return fmt.Sprintf("validation failed: %s", e.Errors[0])
}
