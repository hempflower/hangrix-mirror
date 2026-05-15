// Package infra holds the Postgres-backed implementation of the PAT
// persistence layer. It is intentionally narrow: only the operations
// SQL can answer with one query each, plus pgx error mapping. The
// stateless concerns (regex, bcrypt, secret minting, retry policy)
// live in the sibling service/ package, which composes Repo with
// crypto + wire-format checks to satisfy the broader Store /
// Validator interfaces handlers depend on.
package infra

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/infra/tokendb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresRepo implements domain.Repo on top of a pgx pool.
type PostgresRepo struct {
	q *tokendb.Queries
}

type PostgresRepoDeps struct {
	Pool *pgxpool.Pool
}

func NewPostgresRepo(deps *PostgresRepoDeps) *PostgresRepo {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("token migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_token", "."); err != nil {
		panic(fmt.Errorf("apply token migrations: %w", err))
	}
	return &PostgresRepo{q: tokendb.New(deps.Pool)}
}

// Insert writes a pre-validated, pre-hashed token row. Returns
// domain.ErrPrefixConflict on a unique-violation specifically over
// the prefix column so the service-layer caller can retry with a
// fresh prefix without inspecting pgx error codes itself.
func (r *PostgresRepo) Insert(ctx context.Context, p domain.InsertParams) (*domain.Token, error) {
	scopeStrs := make([]string, 0, len(p.Scopes))
	for _, sc := range p.Scopes {
		scopeStrs = append(scopeStrs, string(sc))
	}
	var expiresArg pgtype.Timestamptz
	if p.ExpiresAt != nil {
		expiresArg = pgtype.Timestamptz{Time: *p.ExpiresAt, Valid: true}
	}
	row, err := r.q.CreateToken(ctx, tokendb.CreateTokenParams{
		UserID:    p.UserID,
		Name:      p.Name,
		Prefix:    p.Prefix,
		HashedKey: p.HashedKey,
		Scopes:    scopeStrs,
		ExpiresAt: expiresArg,
	})
	if err != nil {
		if isPrefixConflict(err) {
			return nil, domain.ErrPrefixConflict
		}
		return nil, err
	}
	return rowToToken(row), nil
}

func (r *PostgresRepo) ListByUser(ctx context.Context, userID int64) ([]*domain.Token, error) {
	rows, err := r.q.ListTokensByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Token, 0, len(rows))
	for i := range rows {
		out = append(out, rowToToken(rows[i]))
	}
	return out, nil
}

func (r *PostgresRepo) Revoke(ctx context.Context, id, userID int64) error {
	n, err := r.q.RevokeToken(ctx, tokendb.RevokeTokenParams{ID: id, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrTokenNotFound
	}
	return nil
}

func (r *PostgresRepo) GetByPrefix(ctx context.Context, prefix string) (*domain.Token, error) {
	row, err := r.q.GetTokenByPrefix(ctx, prefix)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrTokenNotFound
		}
		return nil, err
	}
	return rowToToken(row), nil
}

func (r *PostgresRepo) TouchLastUsed(ctx context.Context, id int64) error {
	return r.q.TouchTokenLastUsed(ctx, id)
}

// ---- helpers ----

func rowToToken(r tokendb.AccessToken) *domain.Token {
	scopes := make([]domain.Scope, 0, len(r.Scopes))
	for _, s := range r.Scopes {
		scopes = append(scopes, domain.Scope(s))
	}
	t := &domain.Token{
		ID:        r.ID,
		UserID:    r.UserID,
		Name:      r.Name,
		Prefix:    r.Prefix,
		HashedKey: r.HashedKey,
		Scopes:    scopes,
		CreatedAt: r.CreatedAt.Time,
	}
	if r.LastUsedAt.Valid {
		v := r.LastUsedAt.Time
		t.LastUsedAt = &v
	}
	if r.ExpiresAt.Valid {
		v := r.ExpiresAt.Time
		t.ExpiresAt = &v
	}
	if r.RevokedAt.Valid {
		v := r.RevokedAt.Time
		t.RevokedAt = &v
	}
	return t
}

// isPrefixConflict reports whether err is a Postgres unique-violation
// on the access_tokens.prefix column specifically. Used by Insert to
// map it to domain.ErrPrefixConflict so service can drive the retry
// loop without leaking pgx specifics.
func isPrefixConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != pgerrcode.UniqueViolation {
		return false
	}
	return strings.Contains(pgErr.ConstraintName, "prefix") ||
		strings.Contains(pgErr.Detail, "prefix")
}
