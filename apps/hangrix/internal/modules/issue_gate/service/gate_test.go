package service

import (
	"context"
	"errors"
	"testing"

	issuegatedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue_gate/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
)

// stubIssueStore implements issuedomain.Store just enough for Gate tests.
type stubIssueStore struct {
	byNumber map[int64]*issuedomain.Issue // keyed by issue number
	err      error
}

func (s *stubIssueStore) GetByNumber(_ context.Context, _ int64, number int64) (*issuedomain.Issue, error) {
	if s.err != nil {
		return nil, s.err
	}
	iss, ok := s.byNumber[number]
	if !ok {
		return nil, issuedomain.ErrIssueNotFound
	}
	return iss, nil
}

// Remaining Store methods panic — the Gate only calls GetByNumber.
func (s *stubIssueStore) Create(context.Context, int64, int64, string, string, string, string, string, int64, int64) (*issuedomain.Issue, error) {
	panic("not implemented")
}
func (s *stubIssueStore) GetByID(context.Context, int64) (*issuedomain.Issue, error)          { panic("not implemented") }
func (s *stubIssueStore) List(context.Context, int64, issuedomain.ListFilter) ([]*issuedomain.Issue, int64, error) {
	panic("not implemented")
}
func (s *stubIssueStore) ListChildren(context.Context, int64) ([]*issuedomain.Issue, error) { panic("not implemented") }
func (s *stubIssueStore) ListOpenDescendants(context.Context, int64) ([]*issuedomain.OpenDescendant, error) {
	panic("not implemented")
}
func (s *stubIssueStore) Plan(context.Context, int64, int64) (*issuedomain.PlanTree, error) { panic("not implemented") }
func (s *stubIssueStore) UpdateTitleBody(context.Context, int64, string, string) (*issuedomain.Issue, error) {
	panic("not implemented")
}
func (s *stubIssueStore) UpdateState(context.Context, int64, issuedomain.State, string) (*issuedomain.Issue, error) {
	panic("not implemented")
}
func (s *stubIssueStore) UpdateHeadSHA(context.Context, int64, string) error { panic("not implemented") }
func (s *stubIssueStore) ListOpenIssueNumbers(context.Context, int64) ([]int64, error) { panic("not implemented") }
func (s *stubIssueStore) CreateComment(context.Context, int64, int64, string, string, string, int) (*issuedomain.Comment, error) {
	panic("not implemented")
}
func (s *stubIssueStore) CreateAgentComment(context.Context, int64, string, string, string, int) (*issuedomain.Comment, error) {
	panic("not implemented")
}
func (s *stubIssueStore) ListComments(context.Context, int64) ([]*issuedomain.Comment, error) { panic("not implemented") }
func (s *stubIssueStore) GetCommentByID(context.Context, int64) (*issuedomain.Comment, error) { panic("not implemented") }
func (s *stubIssueStore) CreateEvent(context.Context, int64, issuedomain.EventKind, []byte, int64, string) (*issuedomain.Event, error) {
	panic("not implemented")
}
func (s *stubIssueStore) CreateAgentEvent(context.Context, int64, issuedomain.EventKind, []byte, string) (*issuedomain.Event, error) {
	panic("not implemented")
}
func (s *stubIssueStore) ListEvents(context.Context, int64) ([]*issuedomain.Event, error) { panic("not implemented") }

func TestGate_CheckIssue_Open(t *testing.T) {
	store := &stubIssueStore{
		byNumber: map[int64]*issuedomain.Issue{
			42: {State: issuedomain.StateOpen},
		},
	}
	g := NewGate(&GateDeps{Issues: store})

	err := g.CheckIssue(context.Background(), 1, 42)
	if err != nil {
		t.Fatalf("expected nil for open issue, got: %v", err)
	}
}

func TestGate_CheckIssue_Closed(t *testing.T) {
	store := &stubIssueStore{
		byNumber: map[int64]*issuedomain.Issue{
			42: {State: issuedomain.StateClosed},
		},
	}
	g := NewGate(&GateDeps{Issues: store})

	err := g.CheckIssue(context.Background(), 1, 42)
	if err == nil {
		t.Fatal("expected error for closed issue, got nil")
	}
	var term *issuegatedomain.ErrIssueTerminal
	if !errors.As(err, &term) {
		t.Fatalf("expected ErrIssueTerminal, got: %v", err)
	}
	if term.State != issuegatedomain.ReasonClosed {
		t.Fatalf("expected state closed, got: %s", term.State)
	}
	if term.IssueNumber != 42 {
		t.Fatalf("expected issue 42, got: %d", term.IssueNumber)
	}
}

func TestGate_CheckIssue_Merged(t *testing.T) {
	store := &stubIssueStore{
		byNumber: map[int64]*issuedomain.Issue{
			7: {State: issuedomain.StateMerged},
		},
	}
	g := NewGate(&GateDeps{Issues: store})

	err := g.CheckIssue(context.Background(), 1, 7)
	if err == nil {
		t.Fatal("expected error for merged issue, got nil")
	}
	var term *issuegatedomain.ErrIssueTerminal
	if !errors.As(err, &term) {
		t.Fatalf("expected ErrIssueTerminal, got: %v", err)
	}
	if term.State != issuegatedomain.ReasonMerged {
		t.Fatalf("expected state merged, got: %s", term.State)
	}
}

func TestGate_CheckIssue_NotFound(t *testing.T) {
	store := &stubIssueStore{byNumber: map[int64]*issuedomain.Issue{}}
	g := NewGate(&GateDeps{Issues: store})

	err := g.CheckIssue(context.Background(), 1, 99)
	if err == nil {
		t.Fatal("expected error for missing issue, got nil")
	}
	if !errors.Is(err, issuedomain.ErrIssueNotFound) {
		t.Fatalf("expected ErrIssueNotFound, got: %v", err)
	}
}

func TestGate_CheckIssue_StoreError(t *testing.T) {
	store := &stubIssueStore{err: errors.New("db down")}
	g := NewGate(&GateDeps{Issues: store})

	err := g.CheckIssue(context.Background(), 1, 42)
	if err == nil {
		t.Fatal("expected error for store failure, got nil")
	}
}
