package infra

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra/repodb"
)

// PostgresMemberStore implements domain.MemberStore on top of the shared pgx
// pool. It is a separate struct from PostgresStore, same as
// PostgresProtectionStore, so method names don't collide.
type PostgresMemberStore struct {
	q *repodb.Queries
}

type PostgresMemberStoreDeps struct {
	Pool *pgxpool.Pool
}

func NewPostgresMemberStore(deps *PostgresMemberStoreDeps) *PostgresMemberStore {
	return &PostgresMemberStore{q: repodb.New(deps.Pool)}
}

func (s *PostgresMemberStore) AddMember(ctx context.Context, repoID, userID, actorID int64, role domain.MemberRole) error {
	err := s.q.AddRepoMember(ctx, repodb.AddRepoMemberParams{
		RepoID:  repoID,
		UserID:  userID,
		Role:    string(role),
		ActorID: actorID,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return domain.ErrRepoMemberConflict
		}
		return err
	}
	return nil
}

func (s *PostgresMemberStore) UpdateMemberRole(ctx context.Context, repoID, userID int64, role domain.MemberRole) error {
	n, err := s.q.UpdateRepoMemberRole(ctx, repodb.UpdateRepoMemberRoleParams{
		RepoID: repoID,
		UserID: userID,
		Role:   string(role),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrRepoMemberNotFound
	}
	return nil
}

func (s *PostgresMemberStore) RemoveMember(ctx context.Context, repoID, userID int64) error {
	n, err := s.q.RemoveRepoMember(ctx, repodb.RemoveRepoMemberParams{
		RepoID: repoID,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrRepoMemberNotFound
	}
	return nil
}

func (s *PostgresMemberStore) ListMembers(ctx context.Context, repoID int64) ([]*domain.RepoMember, error) {
	rows, err := s.q.ListRepoMembers(ctx, repoID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.RepoMember, 0, len(rows))
	for _, r := range rows {
		out = append(out, listMemberRowToDomain(r))
	}
	return out, nil
}

func listMemberRowToDomain(r repodb.ListRepoMembersRow) *domain.RepoMember {
	return &domain.RepoMember{
		RepoID:   r.RepoID,
		UserID:   r.UserID,
		Username: r.Username,
		Role:     domain.MemberRole(r.Role),
		AddedBy:  r.ActorID,
		AddedAt:  r.AddedAt.Time,
	}
}

func (s *PostgresMemberStore) GetMember(ctx context.Context, repoID, userID int64) (*domain.RepoMember, error) {
	row, err := s.q.GetRepoMember(ctx, repodb.GetRepoMemberParams{
		RepoID: repoID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRepoMemberNotFound
		}
		return nil, err
	}
	return memberRowToDomain(row), nil
}

func memberRowToDomain(r repodb.GetRepoMemberRow) *domain.RepoMember {
	return &domain.RepoMember{
		RepoID:   r.RepoID,
		UserID:   r.UserID,
		Username: r.Username,
		Role:     domain.MemberRole(r.Role),
		AddedBy:  r.ActorID,
		AddedAt:  r.AddedAt.Time,
	}
}
