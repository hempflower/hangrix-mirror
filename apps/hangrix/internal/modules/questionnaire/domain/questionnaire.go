// Package domain declares the questionnaire module's types, interfaces,
// and pure-data validation. No I/O, no crypto, no regex on persisted state.
package domain

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ---- Status / Qtype ---- //

// Status is the lifecycle state of a questionnaire.
type Status string

const (
	StatusOpen   Status = "open"
	StatusClosed Status = "closed"
)

func (s Status) Valid() bool { return s == StatusOpen || s == StatusClosed }

// Qtype is the question kind.
type Qtype string

const (
	QtypeSingleChoice Qtype = "single_choice"
	QtypeMultiChoice  Qtype = "multi_choice"
	QtypeTextInput    Qtype = "text_input"
)

func (q Qtype) Valid() bool {
	switch q {
	case QtypeSingleChoice, QtypeMultiChoice, QtypeTextInput:
		return true
	}
	return false
}

// ---- Domain models ---- //

// Option is a single choice option. ID is server-generated (crypto/rand → base32, 8 chars).
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Question is one question in a questionnaire.
type Question struct {
	ID       int64    `json:"id"`
	Position int      `json:"position"`
	Text     string   `json:"text"`
	Type     Qtype    `json:"type"`
	Options  []Option `json:"options,omitempty"` // nil for text_input
	Required bool     `json:"required"`
}

// Questionnaire is the aggregate root.
type Questionnaire struct {
	ID             int64      `json:"id"`
	IssueID        int64      `json:"issue_id"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	Status         Status     `json:"status"`
	CreatedByAgent string     `json:"created_by_agent"`
	CreatedAt      time.Time  `json:"created_at"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
	ClosedReason   string     `json:"closed_reason,omitempty"`
	Questions      []Question `json:"questions,omitempty"`
}

// AnswerValue represents the user's answer to a single question.
type AnswerValue struct {
	OptionIDs []string `json:"option_ids,omitempty"`
	Text      string   `json:"text,omitempty"`
}

// Answer is one user's submission.
type Answer struct {
	ID              int64                 `json:"id"`
	QuestionnaireID int64                 `json:"questionnaire_id"`
	ActorID         int64                 `json:"actor_id"`
	PerQuestion     map[int64]AnswerValue `json:"per_question"`
	SubmittedAt     time.Time             `json:"submitted_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
}

// ---- Result types ---- //

// ChoiceTally is the aggregated count for one option in a choice question.
type ChoiceTally struct {
	OptionID string  `json:"option_id"`
	Label    string  `json:"label"`
	Count    int     `json:"count"`
	Percent  float64 `json:"percent"`
}

// TextResponse is one user's text answer.
type TextResponse struct {
	UserID      int64     `json:"user_id"`
	DisplayName string    `json:"user_display"`
	Text        string    `json:"text"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// Result is the aggregated result of a questionnaire.
type Result struct {
	Questionnaire *Questionnaire           `json:"questionnaire"`
	Submissions   int                      `json:"submissions"`
	ByQuestion    map[int64]QuestionResult `json:"by_question"`
	Submitters    []SubmitterDetail        `json:"submitters,omitempty"`
}

// QuestionResult holds tallies (choice questions) or text responses (text_input).
type QuestionResult struct {
	Type      Qtype          `json:"type"`
	Tallies   []ChoiceTally  `json:"tallies,omitempty"`
	Responses []TextResponse `json:"responses,omitempty"`
}

// SubmitterDetail exposes per-user answers in the results detail view.
type SubmitterDetail struct {
	UserID      int64             `json:"user_id"`
	DisplayName string            `json:"user_display"`
	SubmittedAt time.Time         `json:"submitted_at"`
	Answers     []SubmitterAnswer `json:"answers"`
}

// SubmitterAnswer is one answered question within a submitter detail.
type SubmitterAnswer struct {
	QuestionID int64    `json:"question_id"`
	OptionIDs  []string `json:"option_ids,omitempty"`
	Text       string   `json:"text,omitempty"`
}

// ---- Interfaces ---- //

// Store manages questionnaire and question persistence.
type Store interface {
	Create(ctx context.Context, p CreateParams) (*Questionnaire, error)
	Get(ctx context.Context, id int64) (*Questionnaire, error)
	GetByIssue(ctx context.Context, issueID int64) ([]*Questionnaire, error)
	Close(ctx context.Context, id int64, reason string) (*Questionnaire, error)
}

// AnswerStore manages answer persistence.
type AnswerStore interface {
	// InsertFirstAnswer atomically inserts the answer AND closes the
	// questionnaire, but ONLY if the questionnaire is currently open.
	// Returns (answer, closedQuestionnaire, nil) on success. Returns
	// (nil, nil, ErrQuestionnaireLocked) if the questionnaire was
	// already closed (race-safe — the check + insert + close run in
	// one transaction).
	InsertFirstAnswer(ctx context.Context, qID, actorID int64,
		perQ map[int64]AnswerValue) (*Answer, *Questionnaire, error)

	GetUserAnswer(ctx context.Context, qID, actorID int64) (*Answer, error)
	ListAnswers(ctx context.Context, qID int64) ([]*Answer, error)
	CountAnswers(ctx context.Context, qID int64) (int64, error)
}

// Service is the business-logic layer, composing Store + AnswerStore + result aggregation.
type Service interface {
	Store
	// AnswerStore methods except UpsertAnswer which returns the
	// questionnaire alongside the answer for cross-module orchestration.
	GetUserAnswer(ctx context.Context, qID, actorID int64) (*Answer, error)
	ListAnswers(ctx context.Context, qID int64) ([]*Answer, error)
	CountAnswers(ctx context.Context, qID int64) (int64, error)
	UpsertAnswer(ctx context.Context, qID, actorID int64, perQ map[int64]AnswerValue) (*Answer, *Questionnaire, error)
	BuildResult(ctx context.Context, qID int64) (*Result, error)
}

// ---- Create / input types ---- //

// CreateParams carries the input to Store.Create.
type CreateParams struct {
	IssueID        int64
	Title          string
	Description    string
	CreatedByAgent string
	Questions      []CreateQuestion
}

// CreateQuestion is one question in the creation input.
type CreateQuestion struct {
	Position int
	Text     string
	Type     Qtype
	Options  []Option
	Required bool
}

// ---- Validation ---- //

// Validate checks the questionnaire creation input and returns field-level errors.
func (p *CreateParams) Validate() []FieldError {
	var errs []FieldError

	if len(strings.TrimSpace(p.Title)) == 0 {
		errs = append(errs, FieldError{Field: "title", Code: "missing"})
	} else if len(p.Title) > 200 {
		errs = append(errs, FieldError{Field: "title", Code: "too_long", Message: "max 200 characters"})
	}

	if len(p.Description) > 2000 {
		errs = append(errs, FieldError{Field: "description", Code: "too_long", Message: "max 2000 characters"})
	}

	if len(p.Questions) == 0 {
		errs = append(errs, FieldError{Field: "questions", Code: "missing", Message: "at least one question is required"})
	} else if len(p.Questions) > 20 {
		errs = append(errs, FieldError{Field: "questions", Code: "too_many", Message: "max 20 questions"})
	}

	for i, q := range p.Questions {
		prefix := fieldPrefix("questions", i)

		if len(strings.TrimSpace(q.Text)) == 0 {
			errs = append(errs, FieldError{Field: prefix + ".text", Code: "missing"})
		} else if len(q.Text) > 500 {
			errs = append(errs, FieldError{Field: prefix + ".text", Code: "too_long", Message: "max 500 characters"})
		}

		if !q.Type.Valid() {
			errs = append(errs, FieldError{Field: prefix + ".type", Code: "invalid", Message: "must be single_choice, multi_choice, or text_input"})
		}

		switch q.Type {
		case QtypeSingleChoice, QtypeMultiChoice:
			if len(q.Options) < 2 {
				errs = append(errs, FieldError{Field: prefix + ".options", Code: "too_few", Message: "choice questions need at least 2 options"})
			} else if len(q.Options) > 10 {
				errs = append(errs, FieldError{Field: prefix + ".options", Code: "too_many", Message: "max 10 options"})
			}
			seen := make(map[string]bool)
			for j, o := range q.Options {
				if len(strings.TrimSpace(o.Label)) == 0 {
					errs = append(errs, FieldError{Field: prefix + ".options[" + strconv.Itoa(j) + "].label", Code: "missing"})
				} else if len(o.Label) > 100 {
					errs = append(errs, FieldError{Field: prefix + ".options[" + strconv.Itoa(j) + "].label", Code: "too_long", Message: "max 100 characters"})
				}
				lower := strings.ToLower(strings.TrimSpace(o.Label))
				if seen[lower] {
					errs = append(errs, FieldError{Field: prefix + ".options[" + strconv.Itoa(j) + "].label", Code: "duplicate"})
				}
				seen[lower] = true
			}
		case QtypeTextInput:
			if len(q.Options) > 0 {
				errs = append(errs, FieldError{Field: prefix + ".options", Code: "not_allowed", Message: "options are not allowed for text_input questions"})
			}
		}
	}

	return errs
}

// ValidateAnswer checks a submission against the questionnaire's questions and options.
// Returns field-level errors mapping to the API error codes.
func ValidateAnswer(questions []Question, perQ map[int64]AnswerValue) []FieldError {
	var errs []FieldError

	// Build a lookup of valid question IDs + option IDs.
	qMap := make(map[int64]Question)
	optMap := make(map[int64]map[string]bool) // questionID → optionID set
	for _, q := range questions {
		qMap[q.ID] = q
		if q.Type == QtypeSingleChoice || q.Type == QtypeMultiChoice {
			set := make(map[string]bool)
			for _, o := range q.Options {
				set[o.ID] = true
			}
			optMap[q.ID] = set
		}
	}

	// 1. Required questions must be answered.
	for _, q := range questions {
		if !q.Required {
			continue
		}
		v, ok := perQ[q.ID]
		if !ok {
			errs = append(errs, FieldError{Field: "answers", Code: "missing_required_question", Message: "question " + strconv.FormatInt(q.ID, 10) + " is required"})
		} else {
			switch q.Type {
			case QtypeTextInput:
				if strings.TrimSpace(v.Text) == "" {
					errs = append(errs, FieldError{Field: "answers", Code: "missing_required_question", Message: "question " + strconv.FormatInt(q.ID, 10) + " requires text"})
				}
			case QtypeSingleChoice, QtypeMultiChoice:
				if len(v.OptionIDs) == 0 {
					errs = append(errs, FieldError{Field: "answers", Code: "missing_required_question", Message: "question " + strconv.FormatInt(q.ID, 10) + " requires at least one selection"})
				}
			}
		}
	}
	if len(errs) > 0 {
		return errs
	}

	// 2. Per-answer validation.
	for qID, av := range perQ {
		q, known := qMap[qID]
		if !known {
			errs = append(errs, FieldError{Field: "answers", Code: "unknown_question", Message: "question " + strconv.FormatInt(qID, 10) + " does not exist in this questionnaire"})
			continue
		}

		switch q.Type {
		case QtypeSingleChoice:
			if len(av.OptionIDs) != 1 {
				errs = append(errs, FieldError{Field: "answers", Code: "single_choice_multi_select", Message: "single_choice question " + strconv.FormatInt(qID, 10) + " must have exactly 1 selected option"})
			} else {
				if !optMap[qID][av.OptionIDs[0]] {
					errs = append(errs, FieldError{Field: "answers", Code: "unknown_option", Message: "option " + av.OptionIDs[0] + " is not valid for question " + strconv.FormatInt(qID, 10)})
				}
			}
			if av.Text != "" {
				errs = append(errs, FieldError{Field: "answers", Code: "text_for_choice_question", Message: "text is not allowed for choice question " + strconv.FormatInt(qID, 10)})
			}

		case QtypeMultiChoice:
			if len(av.OptionIDs) < 1 {
				errs = append(errs, FieldError{Field: "answers", Code: "multi_choice_no_select", Message: "multi_choice question " + strconv.FormatInt(qID, 10) + " requires at least 1 selection"})
			}
			for _, oid := range av.OptionIDs {
				if !optMap[qID][oid] {
					errs = append(errs, FieldError{Field: "answers", Code: "unknown_option", Message: "option " + oid + " is not valid for question " + strconv.FormatInt(qID, 10)})
				}
			}
			if av.Text != "" {
				errs = append(errs, FieldError{Field: "answers", Code: "text_for_choice_question", Message: "text is not allowed for choice question " + strconv.FormatInt(qID, 10)})
			}

		case QtypeTextInput:
			if len(av.OptionIDs) > 0 {
				errs = append(errs, FieldError{Field: "answers", Code: "choice_for_text_question", Message: "option_ids are not allowed for text_input question " + strconv.FormatInt(qID, 10)})
			}
			// text can be empty for non-required; required check already done above
		}
	}

	return errs
}

// ---- Error type ---- //

// FieldError describes a single field-level validation failure.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func (e FieldError) Error() string {
	s := e.Field + ": " + e.Code
	if e.Message != "" {
		s += " (" + e.Message + ")"
	}
	return s
}

// ErrQuestionnaireLocked is returned by Service.UpsertAnswer when the
// questionnaire is no longer accepting submissions — either because
// it was explicitly closed, or because a prior submission already
// locked it. Handlers map this to HTTP 409 Conflict.
var ErrQuestionnaireLocked = errors.New("questionnaire is locked and no longer accepting responses")

// ---- Helpers ---- //

func fieldPrefix(base string, idx int) string {
	return base + "[" + strconv.Itoa(idx) + "]"
}
