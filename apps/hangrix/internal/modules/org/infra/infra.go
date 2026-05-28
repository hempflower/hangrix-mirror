// Package infra holds the Postgres-backed implementation of the org domain's
// OrgRepo and Resolver interfaces. Migrations live in migrations/ and are
// applied via the shared database.Migrate helper at construction time. Only
// this package may import the sqlc-generated orgdb subpackage.
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
	actordomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/infra/orgdb"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresRepo implements both OrgRepo and Resolver. Splitting them would
// just duplicate the pool reference; the two interfaces are wired to the
// same instance in the module.
type PostgresRepo struct {
	q     *orgdb.Queries
	users userdomain.Repo
}

type PostgresRepoDeps struct {
	Pool   *pgxpool.Pool
	Users  userdomain.Repo
	// Actors is wired purely for migration ordering: the org module's
	// 00003_add_actor_id_to_organizations.sql has FKs to actors(id), so
	// the actor module's migrations must run first. ioc constructs deps
	// before owners, so depending on the actor store guarantees the
	// right order.
	Actors actordomain.Store
}

func NewPostgresRepo(deps *PostgresRepoDeps) *PostgresRepo {
	_ = deps.Actors // see deps doc comment — referenced for build order only.
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("org migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_org", "."); err != nil {
		panic(fmt.Errorf("apply org migrations: %w", err))
	}
	return &PostgresRepo{q: orgdb.New(deps.Pool), users: deps.Users}
}

// ---- OrgRepo ----

func (r *PostgresRepo) Create(ctx context.Context, name, displayName, description string, actorID int64) (*domain.Org, error) {
	row, err := r.q.CreateOrganization(ctx, orgdb.CreateOrganizationParams{
		Name:        name,
		DisplayName: displayName,
		Description: description,
		ActorID:     actorID,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrOrgConflict
		}
		return nil, err
	}
	return rowToOrg(row), nil
}

func (r *PostgresRepo) GetByName(ctx context.Context, name string) (*domain.Org, error) {
	row, err := r.q.GetOrganizationByName(ctx, name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrOrgNotFound
		}
		return nil, err
	}
	return rowToOrg(row), nil
}

func (r *PostgresRepo) GetByID(ctx context.Context, id int64) (*domain.Org, error) {
	row, err := r.q.GetOrganizationByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrOrgNotFound
		}
		return nil, err
	}
	return rowToOrg(row), nil
}

func (r *PostgresRepo) Exists(ctx context.Context, name string) (bool, error) {
	return r.q.ExistsOrganizationName(ctx, name)
}

func (r *PostgresRepo) UpdateMeta(ctx context.Context, id int64, displayName, description, avatarURL string) (*domain.Org, error) {
	row, err := r.q.UpdateOrganizationMeta(ctx, orgdb.UpdateOrganizationMetaParams{
		ID:          id,
		DisplayName: displayName,
		Description: description,
		AvatarUrl:   avatarURL,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrOrgNotFound
		}
		return nil, err
	}
	return rowToOrg(row), nil
}

func (r *PostgresRepo) SoftDelete(ctx context.Context, id int64) error {
	n, err := r.q.SoftDeleteOrganization(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrOrgNotFound
	}
	return nil
}

func (r *PostgresRepo) AddMember(ctx context.Context, orgID, userID, actorID int64, role domain.Role) error {
	err := r.q.AddOrganizationMember(ctx, orgdb.AddOrganizationMemberParams{
		OrgID:   orgID,
		UserID:  userID,
		Role:    string(role),
		ActorID: actorID,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return domain.ErrMemberConflict
		}
		return err
	}
	return nil
}

func (r *PostgresRepo) UpdateMemberRole(ctx context.Context, orgID, userID int64, role domain.Role) error {
	n, err := r.q.UpdateOrganizationMemberRole(ctx, orgdb.UpdateOrganizationMemberRoleParams{
		OrgID:  orgID,
		UserID: userID,
		Role:   string(role),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrMemberNotFound
	}
	return nil
}

func (r *PostgresRepo) RemoveMember(ctx context.Context, orgID, userID int64) error {
	n, err := r.q.RemoveOrganizationMember(ctx, orgdb.RemoveOrganizationMemberParams{
		OrgID:  orgID,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrMemberNotFound
	}
	return nil
}

func (r *PostgresRepo) ListMembers(ctx context.Context, orgID int64) ([]*domain.Membership, error) {
	rows, err := r.q.ListOrganizationMembers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Membership, 0, len(rows))
	for _, row := range rows {
		out = append(out, &domain.Membership{
			OrgID:    row.OrgID,
			UserID:   row.UserID,
			Username: row.Username,
			Role:     domain.Role(row.Role),
			AddedBy:  row.ActorID,
			AddedAt:  row.AddedAt.Time,
		})
	}
	return out, nil
}

func (r *PostgresRepo) GetMember(ctx context.Context, orgID, userID int64) (*domain.Membership, error) {
	row, err := r.q.GetOrganizationMember(ctx, orgdb.GetOrganizationMemberParams{
		OrgID:  orgID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrMemberNotFound
		}
		return nil, err
	}
	return &domain.Membership{
		OrgID:    row.OrgID,
		UserID:   row.UserID,
		Username: row.Username,
		Role:     domain.Role(row.Role),
		AddedBy:  row.ActorID,
		AddedAt:  row.AddedAt.Time,
	}, nil
}

func (r *PostgresRepo) CountOwners(ctx context.Context, orgID int64) (int64, error) {
	return r.q.CountOrganizationOwners(ctx, orgID)
}

func (r *PostgresRepo) ListOrgsForUser(ctx context.Context, userID int64) ([]*domain.Org, error) {
	rows, err := r.q.ListOrganizationsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Org, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToOrg(row))
	}
	return out, nil
}

// ---- Resolver ----

// ResolveOwner consults the users table first (the more common case) and
// falls back to organizations. Reserved names get rejected before either
// lookup so e.g. /admin/<repo> can never collide with the user-management
// admin route.
func (r *PostgresRepo) ResolveOwner(ctx context.Context, name string) (*domain.Owner, error) {
	if domain.IsReservedName(name) {
		return nil, domain.ErrOrgReserved
	}
	u, err := r.users.GetByUsername(ctx, name)
	if err == nil {
		return &domain.Owner{Kind: domain.OwnerKindUser, ID: u.ID, Name: u.Username}, nil
	}
	if !errors.Is(err, userdomain.ErrUserNotFound) {
		return nil, err
	}
	org, err := r.GetByName(ctx, name)
	if err == nil {
		return &domain.Owner{Kind: domain.OwnerKindOrg, ID: org.ID, Name: org.Name}, nil
	}
	if errors.Is(err, domain.ErrOrgNotFound) {
		return nil, domain.ErrOwnerNotFound
	}
	return nil, err
}

func (r *PostgresRepo) Membership(ctx context.Context, orgID, userID int64) (domain.Role, bool, error) {
	m, err := r.GetMember(ctx, orgID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrMemberNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return m.Role, true, nil
}

// ---- helpers ----

func rowToOrg(r orgdb.Organization) *domain.Org {
	return &domain.Org{
		ID:          r.ID,
		Name:        r.Name,
		DisplayName: r.DisplayName,
		Description: r.Description,
		AvatarURL:   r.AvatarUrl,
		CreatedBy:   r.ActorID,
		CreatedAt:   r.CreatedAt.Time,
		UpdatedAt:   r.UpdatedAt.Time,
	}
}

// nullableInt64 is a small helper to convert *int64 → pgtype.Int8. Kept here
// so other infra files in the repo can grab it via a friendly name without
// pulling pgtype directly.
//
//nolint:unused // exported here to mirror the pgtype helper used by repo/infra
func nullableInt64(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}
