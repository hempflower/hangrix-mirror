// Package infra holds the Postgres-backed implementation of the issue
// domain. SQL lives in queries.sql; sqlc generates the typed accessors
// under issuedb/. This file owns row → domain mapping plus the
// transaction glue for the multi-statement Create flow.
package infra

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/infra/issuedb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore implements domain.Store backed by sqlc-generated queries.
// The pgxpool handle is retained only for the few flows that need an
// explicit transaction (issue creation, where counter UPSERT + insert
// must be atomic).
type PostgresStore struct {
	q    *issuedb.Queries
	pool *pgxpool.Pool
}

type PostgresStoreDeps struct {
	Pool *pgxpool.Pool
}

func NewPostgresStore(deps *PostgresStoreDeps) *PostgresStore {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("issue migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_issue", "."); err != nil {
		panic(fmt.Errorf("apply issue migrations: %w", err))
	}
	return &PostgresStore{
		q:    issuedb.New(deps.Pool),
		pool: deps.Pool,
	}
}

// Create runs the counter UPSERT and the issue insert in one transaction
// so two concurrent creators can never mint the same number. When
// parentID is non-zero the child's parent_id / parent_number columns are
// populated and the caller is expected to have already pointed
// baseBranch at the parent's issue branch.
func (s *PostgresStore) Create(ctx context.Context, repoID, authorID int64, title, body, baseBranch string, agentRole string, parentID, parentNumber int64) (*domain.Issue, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	number, err := qtx.NextIssueNumber(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("issue: bump counter: %w", err)
	}

	// Map the two authorship paths into the DB's mutually exclusive
	// author_id / agent_role columns:
	//   Human: author_id = user ID, agent_role = ""
	//   Agent: author_id = NULL,      agent_role = role key
	var authorArg pgtype.Int8
	if authorID > 0 {
		authorArg = pgtype.Int8{Int64: authorID, Valid: true}
	}

	var parentArg pgtype.Int8
	if parentID > 0 {
		parentArg = pgtype.Int8{Int64: parentID, Valid: true}
	}
	if _, err := qtx.CreateIssue(ctx, issuedb.CreateIssueParams{
		RepoID:       repoID,
		Number:       number,
		AuthorID:     authorArg,
		AgentRole:    agentRole,
		Title:        title,
		Body:         body,
		BranchName:   fmt.Sprintf("issue/%d", number),
		BaseBranch:   baseBranch,
		ParentID:     parentArg,
		ParentNumber: parentNumber,
	}); err != nil {
		return nil, fmt.Errorf("issue: insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetByNumber(ctx, repoID, number)
}

// GetByNumber returns the issue plus author username so the handler can
// serialize a complete record without a follow-up users lookup.
func (s *PostgresStore) GetByNumber(ctx context.Context, repoID, number int64) (*domain.Issue, error) {
	row, err := s.q.GetIssueByNumber(ctx, issuedb.GetIssueByNumberParams{
		RepoID: repoID,
		Number: number,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrIssueNotFound
		}
		return nil, err
	}
	return issueFromGet(row), nil
}

func (s *PostgresStore) List(ctx context.Context, repoID int64, f domain.ListFilter) ([]*domain.Issue, int64, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	var stateArg pgtype.Text
	if f.State != "" {
		stateArg = pgtype.Text{String: string(f.State), Valid: true}
	}
	rows, err := s.q.ListIssues(ctx, issuedb.ListIssuesParams{
		RepoID: repoID,
		State:  stateArg,
		Off:    offset,
		Lim:    limit,
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]*domain.Issue, 0, len(rows))
	for _, r := range rows {
		out = append(out, issueFromList(r))
	}
	total, err := s.q.CountIssues(ctx, issuedb.CountIssuesParams{
		RepoID: repoID,
		State:  stateArg,
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *PostgresStore) ListChildren(ctx context.Context, parentID int64) ([]*domain.Issue, error) {
	rows, err := s.q.ListIssueChildren(ctx, pgtype.Int8{Int64: parentID, Valid: true})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Issue, 0, len(rows))
	for _, r := range rows {
		out = append(out, issueFromChildren(r))
	}
	return out, nil
}

func (s *PostgresStore) UpdateTitleBody(ctx context.Context, id int64, title, body string) (*domain.Issue, error) {
	row, err := s.q.UpdateIssueTitleBody(ctx, issuedb.UpdateIssueTitleBodyParams{
		Title: title,
		Body:  body,
		ID:    id,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrIssueNotFound
		}
		return nil, err
	}
	return s.GetByNumber(ctx, row.RepoID, row.Number)
}

func (s *PostgresStore) UpdateState(ctx context.Context, id int64, state domain.State, mergeSHA string) (*domain.Issue, error) {
	row, err := s.q.UpdateIssueState(ctx, issuedb.UpdateIssueStateParams{
		State:    string(state),
		MergeSha: mergeSHA,
		ID:       id,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrIssueNotFound
		}
		return nil, err
	}
	return s.GetByNumber(ctx, row.RepoID, row.Number)
}

func (s *PostgresStore) UpdateHeadSHA(ctx context.Context, id int64, headSHA string) error {
	return s.q.UpdateIssueHeadSHA(ctx, issuedb.UpdateIssueHeadSHAParams{
		HeadSha: headSHA,
		ID:      id,
	})
}

func (s *PostgresStore) ListOpenIssueNumbers(ctx context.Context, repoID int64) ([]int64, error) {
	return s.q.ListOpenIssueNumbers(ctx, repoID)
}

// CreateComment writes a human-authored comment. authorID is required and
// FKs into users; agent_role is implicitly the empty string. The CHECK
// constraint enforces this XOR at the DB level too.
func (s *PostgresStore) CreateComment(ctx context.Context, issueID, authorID int64, body, filePath string, line int) (*domain.Comment, error) {
	row, err := s.q.CreateComment(ctx, issuedb.CreateCommentParams{
		IssueID:   issueID,
		AuthorID:  pgtype.Int8{Int64: authorID, Valid: true},
		AgentRole: "",
		Body:      body,
		FilePath:  filePath,
		Line:      int32(line),
	})
	if err != nil {
		return nil, err
	}
	return s.commentByID(ctx, row.ID)
}

// CreateAgentComment writes an agent-authored comment. AuthorID is NULL
// in the DB; agent_role carries the host yaml role key. Role-key
// validation belongs in the calling service.
func (s *PostgresStore) CreateAgentComment(ctx context.Context, issueID int64, agentRole, body, filePath string, line int) (*domain.Comment, error) {
	row, err := s.q.CreateComment(ctx, issuedb.CreateCommentParams{
		IssueID:   issueID,
		AuthorID:  pgtype.Int8{}, // NULL — caller is an agent
		AgentRole: agentRole,
		Body:      body,
		FilePath:  filePath,
		Line:      int32(line),
	})
	if err != nil {
		return nil, err
	}
	return s.commentByID(ctx, row.ID)
}

func (s *PostgresStore) commentByID(ctx context.Context, id int64) (*domain.Comment, error) {
	row, err := s.q.GetCommentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return commentFromRow(row), nil
}

func (s *PostgresStore) ListComments(ctx context.Context, issueID int64) ([]*domain.Comment, error) {
	rows, err := s.q.ListComments(ctx, issueID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Comment, 0, len(rows))
	for _, r := range rows {
		out = append(out, &domain.Comment{
			ID:         r.ID,
			IssueID:    r.IssueID,
			AuthorID:   r.AuthorID,
			AuthorName: r.AuthorName,
			AgentRole:  r.AgentRole,
			Body:       r.Body,
			FilePath:   r.FilePath,
			Line:       int(r.Line),
			CreatedAt:  r.CreatedAt.Time,
			UpdatedAt:  r.UpdatedAt.Time,
		})
	}
	return out, nil
}

func (s *PostgresStore) CreateEvent(ctx context.Context, issueID int64, kind domain.EventKind, payload []byte, actorID int64) (*domain.Event, error) {
	var actorArg pgtype.Int8
	if actorID > 0 {
		actorArg = pgtype.Int8{Int64: actorID, Valid: true}
	}
	row, err := s.q.CreateEvent(ctx, issuedb.CreateEventParams{
		IssueID:   issueID,
		Kind:      string(kind),
		Payload:   payload,
		ActorID:   actorArg,
		AgentRole: "",
	})
	if err != nil {
		return nil, err
	}
	return s.eventByID(ctx, row.ID)
}

func (s *PostgresStore) CreateAgentEvent(ctx context.Context, issueID int64, kind domain.EventKind, payload []byte, agentRole string) (*domain.Event, error) {
	row, err := s.q.CreateEvent(ctx, issuedb.CreateEventParams{
		IssueID:   issueID,
		Kind:      string(kind),
		Payload:   payload,
		ActorID:   pgtype.Int8{}, // NULL — agent path
		AgentRole: agentRole,
	})
	if err != nil {
		return nil, err
	}
	return s.eventByID(ctx, row.ID)
}

func (s *PostgresStore) eventByID(ctx context.Context, id int64) (*domain.Event, error) {
	row, err := s.q.GetEventByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return eventFromGet(row), nil
}

func (s *PostgresStore) ListEvents(ctx context.Context, issueID int64) ([]*domain.Event, error) {
	rows, err := s.q.ListEvents(ctx, issueID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, &domain.Event{
			ID:        r.ID,
			IssueID:   r.IssueID,
			Kind:      domain.EventKind(r.Kind),
			Payload:   r.Payload,
			ActorID:   r.ActorID,
			ActorName: r.ActorName,
			AgentRole: r.AgentRole,
			CreatedAt: r.CreatedAt.Time,
		})
	}
	return out, nil
}

// --- row → domain ---

func issueFromGet(r issuedb.GetIssueByNumberRow) *domain.Issue {
	iss := &domain.Issue{
		ID:             r.ID,
		RepoID:         r.RepoID,
		Number:         r.Number,
		AuthorID:       r.AuthorID,
		AuthorName:     r.AuthorName,
		AgentRole:      r.AgentRole,
		Title:          r.Title,
		Body:           r.Body,
		State:          domain.State(r.State),
		BranchName:     r.BranchName,
		BaseBranch:     r.BaseBranch,
		HeadSHA:        r.HeadSha,
		MergeCommitSHA: r.MergeCommitSha,
		ParentID:       r.ParentID,
		ParentNumber:   r.ParentNumber,
		CreatedAt:      r.CreatedAt.Time,
		UpdatedAt:      r.UpdatedAt.Time,
	}
	if r.MergedAt.Valid {
		t := r.MergedAt.Time
		iss.MergedAt = &t
	}
	return iss
}

func issueFromList(r issuedb.ListIssuesRow) *domain.Issue {
	iss := &domain.Issue{
		ID:             r.ID,
		RepoID:         r.RepoID,
		Number:         r.Number,
		AuthorID:       r.AuthorID,
		AuthorName:     r.AuthorName,
		AgentRole:      r.AgentRole,
		Title:          r.Title,
		Body:           r.Body,
		State:          domain.State(r.State),
		BranchName:     r.BranchName,
		BaseBranch:     r.BaseBranch,
		HeadSHA:        r.HeadSha,
		MergeCommitSHA: r.MergeCommitSha,
		ParentID:       r.ParentID,
		ParentNumber:   r.ParentNumber,
		CreatedAt:      r.CreatedAt.Time,
		UpdatedAt:      r.UpdatedAt.Time,
	}
	if r.MergedAt.Valid {
		t := r.MergedAt.Time
		iss.MergedAt = &t
	}
	return iss
}

func issueFromChildren(r issuedb.ListIssueChildrenRow) *domain.Issue {
	iss := &domain.Issue{
		ID:             r.ID,
		RepoID:         r.RepoID,
		Number:         r.Number,
		AuthorID:       r.AuthorID,
		AuthorName:     r.AuthorName,
		AgentRole:      r.AgentRole,
		Title:          r.Title,
		Body:           r.Body,
		State:          domain.State(r.State),
		BranchName:     r.BranchName,
		BaseBranch:     r.BaseBranch,
		HeadSHA:        r.HeadSha,
		MergeCommitSHA: r.MergeCommitSha,
		ParentID:       r.ParentID,
		ParentNumber:   r.ParentNumber,
		CreatedAt:      r.CreatedAt.Time,
		UpdatedAt:      r.UpdatedAt.Time,
	}
	if r.MergedAt.Valid {
		t := r.MergedAt.Time
		iss.MergedAt = &t
	}
	return iss
}

func commentFromRow(r issuedb.GetCommentByIDRow) *domain.Comment {
	return &domain.Comment{
		ID:         r.ID,
		IssueID:    r.IssueID,
		AuthorID:   r.AuthorID,
		AuthorName: r.AuthorName,
		AgentRole:  r.AgentRole,
		Body:       r.Body,
		FilePath:   r.FilePath,
		Line:       int(r.Line),
		CreatedAt:  r.CreatedAt.Time,
		UpdatedAt:  r.UpdatedAt.Time,
	}
}

func eventFromGet(r issuedb.GetEventByIDRow) *domain.Event {
	return &domain.Event{
		ID:        r.ID,
		IssueID:   r.IssueID,
		Kind:      domain.EventKind(r.Kind),
		Payload:   r.Payload,
		ActorID:   r.ActorID,
		ActorName: r.ActorName,
		AgentRole: r.AgentRole,
		CreatedAt: r.CreatedAt.Time,
	}
}


// --- domain.AttachmentStore implementation ---

func (s *PostgresStore) CreateAttachment(ctx context.Context, repoID, issueID, authorID int64, agentRole, storageKey, originalName, displayName string, sizeBytes int64, mimeType, detectedMimeType, sha256 string, kind domain.AttachmentKind, inline bool) (*domain.Attachment, error) {
	var authorArg pgtype.Int8
	if authorID > 0 {
		authorArg = pgtype.Int8{Int64: authorID, Valid: true}
	}
	row, err := s.q.CreateAttachment(ctx, issuedb.CreateAttachmentParams{
		RepoID:           repoID,
		IssueID:          issueID,
		AuthorID:         authorArg,
		AgentRole:        agentRole,
		StorageKey:       storageKey,
		OriginalName:     originalName,
		DisplayName:      displayName,
		SizeBytes:        sizeBytes,
		MimeType:         mimeType,
		DetectedMimeType: detectedMimeType,
		Sha256:           sha256,
		Kind:             string(kind),
		Inline:           inline,
		Status:           string(domain.AttachmentStatusUploaded),
	})
	if err != nil {
		return nil, fmt.Errorf("create attachment: %w", err)
	}
	return s.GetAttachment(ctx, row.ID)
}

func (s *PostgresStore) GetAttachment(ctx context.Context, id int64) (*domain.Attachment, error) {
	row, err := s.q.GetAttachment(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrAttachmentNotFound
		}
		return nil, err
	}
	return attachmentFromRow(row), nil
}

func (s *PostgresStore) ListAttachments(ctx context.Context, issueID, commentID int64) ([]*domain.Attachment, error) {
	var cid pgtype.Int8
	if commentID > 0 {
		cid = pgtype.Int8{Int64: commentID, Valid: true}
	}
	rows, err := s.q.ListAttachments(ctx, issuedb.ListAttachmentsParams{
		IssueID:   issueID,
		CommentID: cid,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Attachment, 0, len(rows))
	for _, r := range rows {
		out = append(out, attachmentFromList(r))
	}
	return out, nil
}

func (s *PostgresStore) MarkAttached(ctx context.Context, id int64, commentID int64) error {
	return s.q.MarkAttachmentAttached(ctx, issuedb.MarkAttachmentAttachedParams{
		ID:        id,
		CommentID: pgtype.Int8{Int64: commentID, Valid: true},
	})
}

func (s *PostgresStore) SoftDeleteAttachment(ctx context.Context, id int64) error {
	return s.q.SoftDeleteAttachment(ctx, id)
}

func attachmentFromRow(r issuedb.GetAttachmentRow) *domain.Attachment {
	a := &domain.Attachment{
		ID:               r.ID,
		RepoID:           r.RepoID,
		IssueID:          r.IssueID,
		CommentID:        r.CommentID,
		AuthorID:         r.AuthorID,
		AgentRole:        r.AgentRole,
		StorageKey:       r.StorageKey,
		OriginalName:     r.OriginalName,
		DisplayName:      r.DisplayName,
		SizeBytes:        r.SizeBytes,
		MimeType:         r.MimeType,
		DetectedMimeType: r.DetectedMimeType,
		SHA256:           r.Sha256,
		Kind:             domain.AttachmentKind(r.Kind),
		Inline:           r.Inline,
		Status:           domain.AttachmentStatus(r.Status),
		CreatedAt:        r.CreatedAt.Time,
	}
	if r.DeletedAt.Valid {
		t := r.DeletedAt.Time
		a.DeletedAt = &t
	}
	return a
}

func attachmentFromList(r issuedb.ListAttachmentsRow) *domain.Attachment {
	a := &domain.Attachment{
		ID:               r.ID,
		RepoID:           r.RepoID,
		IssueID:          r.IssueID,
		CommentID:        r.CommentID,
		AuthorID:         r.AuthorID,
		AgentRole:        r.AgentRole,
		StorageKey:       r.StorageKey,
		OriginalName:     r.OriginalName,
		DisplayName:      r.DisplayName,
		SizeBytes:        r.SizeBytes,
		MimeType:         r.MimeType,
		DetectedMimeType: r.DetectedMimeType,
		SHA256:           r.Sha256,
		Kind:             domain.AttachmentKind(r.Kind),
		Inline:           r.Inline,
		Status:           domain.AttachmentStatus(r.Status),
		CreatedAt:        r.CreatedAt.Time,
	}
	if r.DeletedAt.Valid {
		t := r.DeletedAt.Time
		a.DeletedAt = &t
	}
	return a
}

