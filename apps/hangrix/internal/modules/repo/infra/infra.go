// Package infra holds the Postgres-backed implementation of the repo domain's
// Store interface plus filesystem helpers (Storage) that resolve bare-repo
// paths and delegate creation/deletion to the git module. Migrations live in
// migrations/ and are applied via the shared database.Migrate helper at
// construction time. Only this package may import the sqlc-generated repodb
// subpackage.
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

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra/repodb"
	"github.com/hangrix/hangrix/pkg/cryptobox"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore implements domain.Store on top of a pgx pool via the
// sqlc-generated repodb queries. The struct name encodes the storage backend
// so a future Sqlite or memory impl can sit beside it without ambiguity.
type PostgresStore struct {
	q *repodb.Queries
}

type PostgresStoreDeps struct {
	Pool *pgxpool.Pool
	// Orgs is wired purely for migration ordering: the M5 repo migration
	// adds an `owner_org_id` column with an FK to organizations(id), so the
	// org module's migrations must run first. ioc constructs deps before
	// owners, so depending on the resolver guarantees the right order.
	Orgs orgdomain.Resolver
}

func NewPostgresStore(deps *PostgresStoreDeps) *PostgresStore {
	_ = deps.Orgs // see deps doc comment — referenced for build order only.
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("repo migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_repo", "."); err != nil {
		panic(fmt.Errorf("apply repo migrations: %w", err))
	}
	return &PostgresStore{q: repodb.New(deps.Pool)}
}

func (s *PostgresStore) Create(ctx context.Context, ownerKind domain.OwnerKind, ownerID int64, name, description, defaultBranch string, visibility domain.Visibility) (*domain.Repo, error) {
	var row repodb.Repo
	var err error
	switch ownerKind {
	case domain.OwnerKindUser:
		row, err = s.q.CreateRepoForUser(ctx, repodb.CreateRepoForUserParams{
			OwnerUserID:   pgtype.Int8{Int64: ownerID, Valid: true},
			Name:          name,
			Description:   description,
			Visibility:    string(visibility),
			DefaultBranch: defaultBranch,
		})
	case domain.OwnerKindOrg:
		row, err = s.q.CreateRepoForOrg(ctx, repodb.CreateRepoForOrgParams{
			OwnerOrgID:    pgtype.Int8{Int64: ownerID, Valid: true},
			Name:          name,
			Description:   description,
			Visibility:    string(visibility),
			DefaultBranch: defaultBranch,
		})
	default:
		return nil, domain.ErrInvalidOwnerKind
	}
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrRepoConflict
		}
		return nil, err
	}
	// Create returns the base row without owner_name; fetch the joined view
	// so the caller gets a fully-populated Repo. This is one extra round-
	// trip per create, which is acceptable given how infrequent creates are.
	full, err := s.q.GetRepoByID(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return joinedRowToRepo(full), nil
}

func (s *PostgresStore) GetByID(ctx context.Context, id int64) (*domain.Repo, error) {
	row, err := s.q.GetRepoByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRepoNotFound
		}
		return nil, err
	}
	return joinedRowToRepo(row), nil
}

func (s *PostgresStore) GetByOwnerAndName(ctx context.Context, ownerKind domain.OwnerKind, ownerID int64, name string) (*domain.Repo, error) {
	switch ownerKind {
	case domain.OwnerKindUser:
		row, err := s.q.GetRepoByUserOwnerAndName(ctx, repodb.GetRepoByUserOwnerAndNameParams{
			OwnerUserID: pgtype.Int8{Int64: ownerID, Valid: true},
			Name:        name,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, domain.ErrRepoNotFound
			}
			return nil, err
		}
		return userOwnerRowToRepo(row), nil
	case domain.OwnerKindOrg:
		row, err := s.q.GetRepoByOrgOwnerAndName(ctx, repodb.GetRepoByOrgOwnerAndNameParams{
			OwnerOrgID: pgtype.Int8{Int64: ownerID, Valid: true},
			Name:       name,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, domain.ErrRepoNotFound
			}
			return nil, err
		}
		return orgOwnerRowToRepo(row), nil
	default:
		return nil, domain.ErrInvalidOwnerKind
	}
}

func (s *PostgresStore) ListByOwner(ctx context.Context, ownerKind domain.OwnerKind, ownerID int64, includePrivate bool, offset, limit int32) ([]*domain.Repo, int64, error) {
	switch ownerKind {
	case domain.OwnerKindUser:
		rows, err := s.q.ListReposByUserOwner(ctx, repodb.ListReposByUserOwnerParams{
			OwnerUserID:    pgtype.Int8{Int64: ownerID, Valid: true},
			Limit:          limit,
			Offset:         offset,
			IncludePrivate: includePrivate,
		})
		if err != nil {
			return nil, 0, err
		}
		total, err := s.q.CountReposByUserOwner(ctx, repodb.CountReposByUserOwnerParams{
			OwnerUserID:    pgtype.Int8{Int64: ownerID, Valid: true},
			IncludePrivate: includePrivate,
		})
		if err != nil {
			return nil, 0, err
		}
		out := make([]*domain.Repo, 0, len(rows))
		for _, r := range rows {
			out = append(out, userListRowToRepo(r))
		}
		return out, total, nil
	case domain.OwnerKindOrg:
		rows, err := s.q.ListReposByOrgOwner(ctx, repodb.ListReposByOrgOwnerParams{
			OwnerOrgID:     pgtype.Int8{Int64: ownerID, Valid: true},
			Limit:          limit,
			Offset:         offset,
			IncludePrivate: includePrivate,
		})
		if err != nil {
			return nil, 0, err
		}
		total, err := s.q.CountReposByOrgOwner(ctx, repodb.CountReposByOrgOwnerParams{
			OwnerOrgID:     pgtype.Int8{Int64: ownerID, Valid: true},
			IncludePrivate: includePrivate,
		})
		if err != nil {
			return nil, 0, err
		}
		out := make([]*domain.Repo, 0, len(rows))
		for _, r := range rows {
			out = append(out, orgListRowToRepo(r))
		}
		return out, total, nil
	default:
		return nil, 0, domain.ErrInvalidOwnerKind
	}
}

func (s *PostgresStore) Delete(ctx context.Context, id int64) error {
	n, err := s.q.DeleteRepo(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrRepoNotFound
	}
	return nil
}

func (s *PostgresStore) UpdateMeta(ctx context.Context, id int64, description, defaultBranch string, visibility domain.Visibility) (*domain.Repo, error) {
	_, err := s.q.UpdateRepoMeta(ctx, repodb.UpdateRepoMetaParams{
		ID:            id,
		Description:   description,
		DefaultBranch: defaultBranch,
		Visibility:    string(visibility),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRepoNotFound
		}
		return nil, err
	}
	full, err := s.q.GetRepoByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRepoNotFound
		}
		return nil, err
	}
	return joinedRowToRepo(full), nil
}

func (s *PostgresStore) Transfer(ctx context.Context, id int64, newOwnerKind domain.OwnerKind, newOwnerID int64) (*domain.Repo, error) {
	var n int64
	var err error
	switch newOwnerKind {
	case domain.OwnerKindUser:
		n, err = s.q.TransferRepoToUser(ctx, repodb.TransferRepoToUserParams{
			ID:          id,
			OwnerUserID: pgtype.Int8{Int64: newOwnerID, Valid: true},
		})
	case domain.OwnerKindOrg:
		n, err = s.q.TransferRepoToOrg(ctx, repodb.TransferRepoToOrgParams{
			ID:         id,
			OwnerOrgID: pgtype.Int8{Int64: newOwnerID, Valid: true},
		})
	default:
		return nil, domain.ErrInvalidOwnerKind
	}
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrRepoConflict
		}
		return nil, err
	}
	if n == 0 {
		return nil, domain.ErrRepoNotFound
	}
	full, err := s.q.GetRepoByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return joinedRowToRepo(full), nil
}

// ---- row mappers ----

func joinedRowToRepo(r repodb.GetRepoByIDRow) *domain.Repo {
	out := &domain.Repo{
		ID:            r.ID,
		OwnerKind:     domain.OwnerKind(r.OwnerKind),
		OwnerName:     r.OwnerName,
		Name:          r.Name,
		Description:   r.Description,
		Visibility:    domain.Visibility(r.Visibility),
		DefaultBranch: r.DefaultBranch,
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
	}
	if r.OwnerUserID.Valid {
		out.OwnerID = r.OwnerUserID.Int64
	} else if r.OwnerOrgID.Valid {
		out.OwnerID = r.OwnerOrgID.Int64
	}
	return out
}

func userOwnerRowToRepo(r repodb.GetRepoByUserOwnerAndNameRow) *domain.Repo {
	out := &domain.Repo{
		ID:            r.ID,
		OwnerKind:     domain.OwnerKind(r.OwnerKind),
		OwnerName:     r.OwnerName,
		Name:          r.Name,
		Description:   r.Description,
		Visibility:    domain.Visibility(r.Visibility),
		DefaultBranch: r.DefaultBranch,
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
	}
	if r.OwnerUserID.Valid {
		out.OwnerID = r.OwnerUserID.Int64
	}
	return out
}

func orgOwnerRowToRepo(r repodb.GetRepoByOrgOwnerAndNameRow) *domain.Repo {
	out := &domain.Repo{
		ID:            r.ID,
		OwnerKind:     domain.OwnerKind(r.OwnerKind),
		OwnerName:     r.OwnerName,
		Name:          r.Name,
		Description:   r.Description,
		Visibility:    domain.Visibility(r.Visibility),
		DefaultBranch: r.DefaultBranch,
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
	}
	if r.OwnerOrgID.Valid {
		out.OwnerID = r.OwnerOrgID.Int64
	}
	return out
}

func userListRowToRepo(r repodb.ListReposByUserOwnerRow) *domain.Repo {
	out := &domain.Repo{
		ID:            r.ID,
		OwnerKind:     domain.OwnerKind(r.OwnerKind),
		OwnerName:     r.OwnerName,
		Name:          r.Name,
		Description:   r.Description,
		Visibility:    domain.Visibility(r.Visibility),
		DefaultBranch: r.DefaultBranch,
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
	}
	if r.OwnerUserID.Valid {
		out.OwnerID = r.OwnerUserID.Int64
	}
	return out
}

func orgListRowToRepo(r repodb.ListReposByOrgOwnerRow) *domain.Repo {
	out := &domain.Repo{
		ID:            r.ID,
		OwnerKind:     domain.OwnerKind(r.OwnerKind),
		OwnerName:     r.OwnerName,
		Name:          r.Name,
		Description:   r.Description,
		Visibility:    domain.Visibility(r.Visibility),
		DefaultBranch: r.DefaultBranch,
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
	}
	if r.OwnerOrgID.Valid {
		out.OwnerID = r.OwnerOrgID.Int64
	}
	return out
}

// ---- Repo variables ----

// PostgresVariableStore implements domain.VariableStore, backed by the
// sqlc-generated repodb queries. Secret values are encrypted with
// AES-256-GCM using the platform's encryption key before storage and
// decrypted on read; callers always deal in plaintext.
type PostgresVariableStore struct {
	q   *repodb.Queries
	box *cryptobox.Box
}

type PostgresVariableStoreDeps struct {
	Pool   *pgxpool.Pool
	Config *config.Config
}

func NewPostgresVariableStore(deps *PostgresVariableStoreDeps) *PostgresVariableStore {
	box, err := cryptobox.New(deps.Config.LLM.EncryptionKey)
	if err != nil {
		panic(fmt.Errorf("repo variable store: init encryption: %w", err))
	}
	return &PostgresVariableStore{
		q:   repodb.New(deps.Pool),
		box: box,
	}
}

func (s *PostgresVariableStore) List(ctx context.Context, repoID int64) ([]*domain.RepoVariable, error) {
	rows, err := s.q.ListRepoVariables(ctx, repoID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.RepoVariable, 0, len(rows))
	for _, r := range rows {
		v, err := s.rowToDomain(&r)
		if err != nil {
			// Decryption failed — return a metadata-only entry so the
			// admin can still see and delete/re-seed the variable.
			// The empty Value and DecryptionFailed=true ensure:
			//  - secret PATCH "keep old value" will fail via Get() rather
			//    than silently overwrite the corrupted ciphertext, and
			//  - runner dispatch will skip this entry so ${NAME} fails
			//    explicitly instead of expanding to an empty string.
			v = &domain.RepoVariable{
				ID:               r.ID,
				RepoID:           r.RepoID,
				Name:             r.Name,
				Value:            "",
				Kind:             domain.VariableKind(r.Kind),
				DecryptionFailed: true,
				CreatedAt:        r.CreatedAt.Time,
				UpdatedAt:        r.UpdatedAt.Time,
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *PostgresVariableStore) Get(ctx context.Context, id, repoID int64) (*domain.RepoVariable, error) {
	row, err := s.q.GetRepoVariable(ctx, repodb.GetRepoVariableParams{ID: id, RepoID: repoID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrVariableNotFound
		}
		return nil, err
	}
	return s.rowToDomain(&row)
}

func (s *PostgresVariableStore) Create(ctx context.Context, repoID int64, name, value string, kind domain.VariableKind) (*domain.RepoVariable, error) {
	if !kind.Valid() {
		return nil, domain.ErrVariableKindInvalid
	}
	if name == "" {
		return nil, domain.ErrVariableNameEmpty
	}
	if !isValidVariableName(name) {
		return nil, domain.ErrVariableNameInvalid
	}

	storedValue := value
	if kind == domain.VariableKindSecret {
		sealed, err := s.box.Encrypt(value)
		if err != nil {
			return nil, fmt.Errorf("encrypt variable: %w", err)
		}
		storedValue = sealed
	}

	row, err := s.q.CreateRepoVariable(ctx, repodb.CreateRepoVariableParams{
		RepoID: repoID,
		Name:   name,
		Value:  storedValue,
		Kind:   string(kind),
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrVariableConflict
		}
		return nil, err
	}
	return s.rowToDomain(&row)
}

func (s *PostgresVariableStore) Update(ctx context.Context, id, repoID int64, name, value string, kind domain.VariableKind) (*domain.RepoVariable, error) {
	if !kind.Valid() {
		return nil, domain.ErrVariableKindInvalid
	}
	if name == "" {
		return nil, domain.ErrVariableNameEmpty
	}
	if !isValidVariableName(name) {
		return nil, domain.ErrVariableNameInvalid
	}

	storedValue := value
	if kind == domain.VariableKindSecret {
		sealed, err := s.box.Encrypt(value)
		if err != nil {
			return nil, fmt.Errorf("encrypt variable: %w", err)
		}
		storedValue = sealed
	}

	row, err := s.q.UpdateRepoVariable(ctx, repodb.UpdateRepoVariableParams{
		ID:     id,
		RepoID: repoID,
		Name:   name,
		Value:  storedValue,
		Kind:   string(kind),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrVariableNotFound
		}
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrVariableConflict
		}
		return nil, err
	}
	return s.rowToDomain(&row)
}

func (s *PostgresVariableStore) Delete(ctx context.Context, id, repoID int64) error {
	n, err := s.q.DeleteRepoVariable(ctx, repodb.DeleteRepoVariableParams{ID: id, RepoID: repoID})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrVariableNotFound
	}
	return nil
}

// rowToDomain decrypts secret values and returns a plaintext domain object.
// On decryption failure (key rotation, corrupted ciphertext) it returns
// ErrVariableDecryptionFailed so callers can decide whether to skip or fail.
func (s *PostgresVariableStore) rowToDomain(r *repodb.RepoVariable) (*domain.RepoVariable, error) {
	value := r.Value
	kind := domain.VariableKind(r.Kind)
	if kind == domain.VariableKindSecret && value != "" {
		plain, err := s.box.Decrypt(value)
		if err != nil {
			return nil, domain.ErrVariableDecryptionFailed
		}
		value = plain
	}
	return &domain.RepoVariable{
		ID:        r.ID,
		RepoID:    r.RepoID,
		Name:      r.Name,
		Value:     value,
		Kind:      kind,
		CreatedAt: r.CreatedAt.Time,
		UpdatedAt: r.UpdatedAt.Time,
	}, nil
}

// isValidVariableName enforces the same env-key shape as agents.yml
// container.env: uppercase, `[A-Z_][A-Z0-9_]*`. Shared with
// agentsconfig.isValidEnvKey in spirit; duplicated here to avoid a
// cross-module import.
func isValidVariableName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
