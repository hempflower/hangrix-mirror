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

	"github.com/hangrix/hangrix/pkg/actor"

	actordomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/infra/issuedb"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore implements domain.Store backed by sqlc-generated queries.
// The pgxpool handle is retained only for the few flows that need an
// explicit transaction (issue creation, where counter UPSERT + insert
// must be atomic).
type PostgresStore struct {
	q          *issuedb.Queries
	pool       *pgxpool.Pool
	actorStore actordomain.Store
}

type PostgresStoreDeps struct {
	Pool       *pgxpool.Pool
	ActorStore actordomain.Store
	// Repos is wired purely for migration ordering: the issue module's
	// 00001_create_issues.sql has a FK to repos(id), so the repo module's
	// migrations must run first. ioc constructs deps before owners, so
	// depending on the repo store guarantees the right order.
	Repos repodomain.Store
}

// resolvedActor resolves an actor_id from legacy authorID/agentRole params.
// When authorID > 0 it ensures a user actor; when agentRole is set it ensures
// an agent actor; otherwise it falls back to the system actor.
func (s *PostgresStore) resolvedActor(ctx context.Context, authorID int64, agentRole string) (int64, error) {
	switch {
	case authorID > 0:
		a, err := s.actorStore.EnsureUser(ctx, authorID, "")
		if err != nil {
			return 0, fmt.Errorf("ensure user actor %d: %w", authorID, err)
		}
		return a.ActorID, nil
	case agentRole != "":
		a, err := s.actorStore.EnsureAgentRole(ctx, agentRole)
		if err != nil {
			return 0, fmt.Errorf("ensure agent actor %s: %w", agentRole, err)
		}
		return a.ActorID, nil
	default:
		// System actor (id=1) — must exist (seeded by migration).
		a, err := s.actorStore.GetByID(ctx, 1)
		if err != nil {
			return 0, fmt.Errorf("get system actor: %w", err)
		}
		return a.ActorID, nil
	}
}

func NewPostgresStore(deps *PostgresStoreDeps) *PostgresStore {
	_ = deps.Repos // see deps doc comment — referenced for build order only.
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("issue migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_issue", "."); err != nil {
		panic(fmt.Errorf("apply issue migrations: %w", err))
	}
	return &PostgresStore{
		q:          issuedb.New(deps.Pool),
		pool:       deps.Pool,
		actorStore: deps.ActorStore,
	}
}

// Create runs the counter UPSERT and the issue insert in one transaction
// so two concurrent creators can never mint the same number. When
// parentID is non-zero the child's parent_id / parent_number columns are
// populated and the caller is expected to have already pointed
// baseBranch at the parent's issue branch.
func (s *PostgresStore) Create(ctx context.Context, repoID, authorID int64, authorName, title, body, baseBranch string, agentRole string, parentID, parentNumber int64) (*domain.Issue, error) {
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

	// Phase 3d: resolve actor_id from legacy authorID/agentRole.
	actorID, err := s.resolvedActor(ctx, authorID, agentRole)
	if err != nil {
		return nil, fmt.Errorf("issue: resolve actor: %w", err)
	}

	var parentArg pgtype.Int8
	if parentID > 0 {
		parentArg = pgtype.Int8{Int64: parentID, Valid: true}
	}
	if _, err := qtx.CreateIssue(ctx, issuedb.CreateIssueParams{
		RepoID:       repoID,
		Number:       number,
		ActorID:      actorID,
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

// GetByID returns the issue by its internal row ID.
func (s *PostgresStore) GetByID(ctx context.Context, id int64) (*domain.Issue, error) {
	row, err := s.q.GetIssueByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrIssueNotFound
		}
		return nil, err
	}
	return issueFromGetByID(row), nil
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

func (s *PostgresStore) ListOpenDescendants(ctx context.Context, rootID int64) ([]*domain.OpenDescendant, error) {
	rows, err := s.q.ListOpenDescendantIssues(ctx, pgtype.Int8{Int64: rootID, Valid: true})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.OpenDescendant, 0, len(rows))
	for _, r := range rows {
		out = append(out, &domain.OpenDescendant{
			ID:     r.ID,
			Number: r.Number,
			Title:  r.Title,
			State:  domain.State(r.State),
			Depth:  int(r.Depth),
		})
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

// CreateComment writes a human-authored comment. authorID is used to resolve
// the actor; agent_role is implicitly the empty string.
func (s *PostgresStore) CreateComment(ctx context.Context, issueID, authorID int64, authorName, body, filePath string, line int) (*domain.Comment, error) {
	actorID, err := s.resolvedActor(ctx, authorID, "")
	if err != nil {
		return nil, fmt.Errorf("comment: resolve actor: %w", err)
	}
	row, err := s.q.CreateComment(ctx, issuedb.CreateCommentParams{
		IssueID:  issueID,
		ActorID:  actorID,
		Body:     body,
		FilePath: filePath,
		Line:     int32(line),
	})
	if err != nil {
		return nil, err
	}
	return s.GetCommentByID(ctx, row.ID)
}

// CreateAgentComment writes an agent-authored comment. agentRole carries
// the host yaml role key. Role-key validation belongs in the calling service.
func (s *PostgresStore) CreateAgentComment(ctx context.Context, issueID int64, agentRole, body, filePath string, line int) (*domain.Comment, error) {
	actorID, err := s.resolvedActor(ctx, 0, agentRole)
	if err != nil {
		return nil, fmt.Errorf("agent comment: resolve actor: %w", err)
	}
	row, err := s.q.CreateComment(ctx, issuedb.CreateCommentParams{
		IssueID:  issueID,
		ActorID:  actorID,
		Body:     body,
		FilePath: filePath,
		Line:     int32(line),
	})
	if err != nil {
		return nil, err
	}
	return s.GetCommentByID(ctx, row.ID)
}

func (s *PostgresStore) GetCommentByID(ctx context.Context, id int64) (*domain.Comment, error) {
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
			ActorID:    r.ActorID,
			Actor:      actor.RefFromColumns(actor.Kind(r.ActorKind), r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName),
			Body:       r.Body,
			FilePath:   r.FilePath,
			Line:       int(r.Line),
			CreatedAt:  r.CreatedAt.Time,
			UpdatedAt:  r.UpdatedAt.Time,
		})
	}
	return out, nil
}

func (s *PostgresStore) CreateEvent(ctx context.Context, issueID int64, kind domain.EventKind, payload []byte, actorUserID int64, actorName string) (*domain.Event, error) {
	actorID, err := s.resolvedActor(ctx, actorUserID, "")
	if err != nil {
		return nil, fmt.Errorf("event: resolve actor: %w", err)
	}
	row, err := s.q.CreateEvent(ctx, issuedb.CreateEventParams{
		IssueID: issueID,
		Kind:    string(kind),
		Payload: payload,
		ActorID: actorID,
	})
	if err != nil {
		return nil, err
	}
	return s.eventByID(ctx, row.ID)
}

func (s *PostgresStore) CreateAgentEvent(ctx context.Context, issueID int64, kind domain.EventKind, payload []byte, agentRole string) (*domain.Event, error) {
	actorID, err := s.resolvedActor(ctx, 0, agentRole)
	if err != nil {
		return nil, fmt.Errorf("agent event: resolve actor: %w", err)
	}
	row, err := s.q.CreateEvent(ctx, issuedb.CreateEventParams{
		IssueID: issueID,
		Kind:    string(kind),
		Payload: payload,
		ActorID: actorID,
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
			Actor:     actor.RefFromColumns(actor.Kind(r.ActorKind), r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName),
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
		ActorID:        r.ActorID,
		Actor:          actor.RefFromColumns(actor.Kind(r.ActorKind), r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName),
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

func issueFromGetByID(r issuedb.GetIssueByIDRow) *domain.Issue {
	iss := &domain.Issue{
		ID:             r.ID,
		RepoID:         r.RepoID,
		Number:         r.Number,
		AuthorID:       r.AuthorID,
		AuthorName:     r.AuthorName,
		AgentRole:      r.AgentRole,
		ActorID:        r.ActorID,
		Actor:          actor.RefFromColumns(actor.Kind(r.ActorKind), r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName),
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
		ActorID:        r.ActorID,
		Actor:          actor.RefFromColumns(actor.Kind(r.ActorKind), r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName),
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
		ActorID:        r.ActorID,
		Actor:          actor.RefFromColumns(actor.Kind(r.ActorKind), r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName),
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
		ActorID:    r.ActorID,
		Actor:      actor.RefFromColumns(actor.Kind(r.ActorKind), r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName),
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
		Actor:     actor.RefFromColumns(actor.Kind(r.ActorKind), r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName),
		CreatedAt: r.CreatedAt.Time,
	}
}

// --- domain.AttachmentStore implementation ---

func (s *PostgresStore) CreateAttachment(ctx context.Context, repoID, issueID, authorID int64, authorName, agentRole, storageKey, originalName, displayName string, sizeBytes int64, mimeType, detectedMimeType, sha256 string, kind domain.AttachmentKind, inline bool) (*domain.Attachment, error) {
	actorID, err := s.resolvedActor(ctx, authorID, agentRole)
	if err != nil {
		return nil, fmt.Errorf("attachment: resolve actor: %w", err)
	}
	row, err := s.q.CreateAttachment(ctx, issuedb.CreateAttachmentParams{
		RepoID:           repoID,
		IssueID:          issueID,
		ActorID:          actorID,
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
		Actor:            actor.RefFromColumns(actor.Kind(r.ActorKind), r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName),
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
		Actor:            actor.RefFromColumns(actor.Kind(r.ActorKind), r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName),
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

// --- domain.ContributionStore implementation ---

func (s *PostgresStore) UpsertContributionOnPush(ctx context.Context, p domain.ContributionUpsertParams) (*domain.Contribution, error) {
	// changed_paths is TEXT[] NOT NULL. pgx encodes a nil []string as SQL
	// NULL (not an empty array '{}'), so a nil here would fail the NOT NULL
	// constraint and the whole upsert — meaning a pushed contribution branch
	// with an empty/uncomputable diff (DiffMergeBase error or no changes)
	// would never get a row and so never be recognised. Coalesce to an empty
	// non-nil slice, which pgx encodes as '{}'.
	changedPaths := p.ChangedPaths
	if changedPaths == nil {
		changedPaths = []string{}
	}
	actorID, err := s.resolvedActor(ctx, 0, p.AgentRole)
	if err != nil {
		return nil, fmt.Errorf("contribution: resolve actor: %w", err)
	}
	id, err := s.q.UpsertContributionOnPush(ctx, issuedb.UpsertContributionOnPushParams{
		RepoID:       p.RepoID,
		IssueID:      p.IssueID,
		SessionID:    p.SessionID,
		AgentRole:    p.AgentRole,
		RefName:      p.RefName,
		HeadSha:      p.HeadSHA,
		BaseSha:      p.BaseSHA,
		ChangedPaths: changedPaths,
		Files:        p.Files,
		Additions:    p.Additions,
		Deletions:    p.Deletions,
		ActorID:      actorID,
	})
	if err != nil {
		return nil, fmt.Errorf("upsert contribution: %w", err)
	}
	return s.GetContribution(ctx, id)
}

func (s *PostgresStore) GetContribution(ctx context.Context, id int64) (*domain.Contribution, error) {
	row, err := s.q.GetContribution(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrContributionNotFound
		}
		return nil, err
	}
	return contributionFromGet(row), nil
}

func (s *PostgresStore) GetContributionByRef(ctx context.Context, issueID int64, refName string) (*domain.Contribution, error) {
	row, err := s.q.GetContributionByRef(ctx, issuedb.GetContributionByRefParams{IssueID: issueID, RefName: refName})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrContributionNotFound
		}
		return nil, err
	}
	return contributionFromGetByRef(row), nil
}

func (s *PostgresStore) ListContributions(ctx context.Context, issueID int64, includeClosed, includeMerged bool) ([]*domain.Contribution, error) {
	rows, err := s.q.ListContributions(ctx, issuedb.ListContributionsParams{
		IssueID:       issueID,
		IncludeClosed: includeClosed,
		IncludeMerged: includeMerged,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Contribution, 0, len(rows))
	for _, r := range rows {
		out = append(out, contributionFromList(r))
	}
	return out, nil
}

func (s *PostgresStore) SetContributionMeta(ctx context.Context, id int64, title, description string) (*domain.Contribution, error) {
	if _, err := s.q.SetContributionMeta(ctx, issuedb.SetContributionMetaParams{ID: id, Title: title, Description: description}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrContributionNotFound
		}
		return nil, err
	}
	return s.GetContribution(ctx, id)
}

func (s *PostgresStore) SetContributionStatus(ctx context.Context, id int64, status domain.ContributionStatus) (*domain.Contribution, error) {
	if _, err := s.q.SetContributionStatus(ctx, issuedb.SetContributionStatusParams{ID: id, Status: string(status)}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrContributionNotFound
		}
		return nil, err
	}
	return s.GetContribution(ctx, id)
}

func (s *PostgresStore) SetContributionMergeable(ctx context.Context, id int64, mergeable bool, mode string) error {
	return s.q.SetContributionMergeable(ctx, issuedb.SetContributionMergeableParams{ID: id, Mergeable: mergeable, MergeMode: mode})
}

func (s *PostgresStore) MarkContributionMerged(ctx context.Context, id int64, mergedCommitSHA string) (*domain.Contribution, error) {
	if _, err := s.q.MarkContributionMerged(ctx, issuedb.MarkContributionMergedParams{ID: id, MergedCommitSha: mergedCommitSHA}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrContributionNotFound
		}
		return nil, err
	}
	return s.GetContribution(ctx, id)
}


func contributionFromGet(r issuedb.GetContributionRow) *domain.Contribution {
	return buildContribution(
		r.ID, r.RepoID, r.IssueID, r.SessionID, r.AgentRole, r.RefName,
		r.HeadSha, r.BaseSha, r.Title, r.Description, r.Status,
		r.Mergeable, r.MergeMode, r.ChangedPaths, r.Files, r.Additions, r.Deletions,
		r.MergedCommitSha, r.MergedAt, r.CreatedAt, r.UpdatedAt,
		r.ActorKind, r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName,
	)
}

func contributionFromGetByRef(r issuedb.GetContributionByRefRow) *domain.Contribution {
	return buildContribution(
		r.ID, r.RepoID, r.IssueID, r.SessionID, r.AgentRole, r.RefName,
		r.HeadSha, r.BaseSha, r.Title, r.Description, r.Status,
		r.Mergeable, r.MergeMode, r.ChangedPaths, r.Files, r.Additions, r.Deletions,
		r.MergedCommitSha, r.MergedAt, r.CreatedAt, r.UpdatedAt,
		r.ActorKind, r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName,
	)
}

func contributionFromList(r issuedb.ListContributionsRow) *domain.Contribution {
	return buildContribution(
		r.ID, r.RepoID, r.IssueID, r.SessionID, r.AgentRole, r.RefName,
		r.HeadSha, r.BaseSha, r.Title, r.Description, r.Status,
		r.Mergeable, r.MergeMode, r.ChangedPaths, r.Files, r.Additions, r.Deletions,
		r.MergedCommitSha, r.MergedAt, r.CreatedAt, r.UpdatedAt,
		r.ActorKind, r.ActorUserID, r.ActorRoleKey, r.ActorWorkflowRunID, r.ActorDisplayName,
	)
}

func buildContribution(
	id, repoID, issueID, sessionID int64, agentRole, refName, headSha, baseSha, title, description, status string,
	mergeable bool, mergeMode string, changedPaths []string, files, additions, deletions int32,
	mergedCommitSha string, mergedAt, createdAt, updatedAt pgtype.Timestamptz,
	actorKind string, actorUserID int64, actorRoleKey string, actorWorkflowRunID int64, actorDisplayName string,
) *domain.Contribution {
	c := &domain.Contribution{
		ID:              id,
		RepoID:          repoID,
		IssueID:         issueID,
		SessionID:       sessionID,
		AgentRole:       agentRole,
		Actor:           actor.RefFromColumns(actor.Kind(actorKind), actorUserID, actorRoleKey, actorWorkflowRunID, actorDisplayName),
		RefName:         refName,
		HeadSHA:         headSha,
		BaseSHA:         baseSha,
		Title:           title,
		Description:     description,
		Status:          domain.ContributionStatus(status),
		Mergeable:       mergeable,
		MergeMode:       mergeMode,
		ChangedPaths:    changedPaths,
		Files:           files,
		Additions:       additions,
		Deletions:       deletions,
		MergedCommitSHA: mergedCommitSha,
		CreatedAt:       createdAt.Time,
		UpdatedAt:       updatedAt.Time,
	}
	if mergedAt.Valid {
		t := mergedAt.Time
		c.MergedAt = &t
	}
	return c
}

// Ensure PostgresStore implements domain.ContributionStore.
var _ domain.ContributionStore = (*PostgresStore)(nil)

// --- domain.TodoStore implementation ---

func (s *PostgresStore) ListTodos(ctx context.Context, issueID int64) ([]*domain.Todo, error) {
	rows, err := s.q.ListTodos(ctx, issueID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Todo, 0, len(rows))
	for _, r := range rows {
		out = append(out, todoFromRow(r))
	}
	return out, nil
}

func (s *PostgresStore) GetTodo(ctx context.Context, id int64) (*domain.Todo, error) {
	row, err := s.q.GetTodoByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrTodoNotFound
		}
		return nil, fmt.Errorf("get todo: %w", err)
	}
	return todoFromRow(row), nil
}

func (s *PostgresStore) CreateTodo(ctx context.Context, issueID int64, content string, status domain.TodoStatus, position int) (*domain.Todo, error) {
	row, err := s.q.CreateTodo(ctx, issuedb.CreateTodoParams{
		IssueID:  issueID,
		Content:  content,
		Status:   string(status),
		Position: int32(position),
	})
	if err != nil {
		return nil, fmt.Errorf("create todo: %w", err)
	}
	return todoFromRow(row), nil
}

func (s *PostgresStore) UpdateTodoStatus(ctx context.Context, id int64, status domain.TodoStatus, content *string) (*domain.Todo, error) {
	var contentArg pgtype.Text
	if content != nil {
		contentArg = pgtype.Text{String: *content, Valid: true}
	}
	row, err := s.q.UpdateTodoStatus(ctx, issuedb.UpdateTodoStatusParams{
		ID:      id,
		Status:  string(status),
		Content: contentArg,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrTodoNotFound
		}
		return nil, fmt.Errorf("update todo status: %w", err)
	}
	return todoFromRow(row), nil
}

func (s *PostgresStore) UpdateTodoContent(ctx context.Context, id int64, content string) (*domain.Todo, error) {
	row, err := s.q.UpdateTodoContent(ctx, issuedb.UpdateTodoContentParams{
		ID:      id,
		Content: content,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrTodoNotFound
		}
		return nil, fmt.Errorf("update todo content: %w", err)
	}
	return todoFromRow(row), nil
}

func (s *PostgresStore) DeleteTodo(ctx context.Context, id int64) error {
	return s.q.DeleteTodo(ctx, id)
}

func (s *PostgresStore) CountTodosByStatus(ctx context.Context, issueID int64) (*domain.TodoSummary, error) {
	rows, err := s.q.CountTodosByStatus(ctx, issueID)
	if err != nil {
		return nil, err
	}
	sum := &domain.TodoSummary{}
	for _, r := range rows {
		switch r.Status {
		case string(domain.TodoStatusTodo):
			sum.Todo = r.Count
		case string(domain.TodoStatusInProgress):
			sum.InProgress = r.Count
		case string(domain.TodoStatusDone):
			sum.Done = r.Count
		}
		sum.Total += r.Count
	}
	return sum, nil
}

func todoFromRow(r issuedb.Todo) *domain.Todo {
	return &domain.Todo{
		ID:        r.ID,
		IssueID:   r.IssueID,
		Content:   r.Content,
		Status:    domain.TodoStatus(r.Status),
		Position:  int(r.Position),
		CreatedAt: r.CreatedAt.Time,
		UpdatedAt: r.UpdatedAt.Time,
	}
}

// Ensure PostgresStore implements domain.TodoStore.
var _ domain.TodoStore = (*PostgresStore)(nil)
