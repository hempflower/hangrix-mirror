// Package infra holds the Postgres-backed implementation of the token domain's
// Store and Validator interfaces. Migrations live in migrations/ and are
// applied via the shared database.Migrate helper at construction time. Only
// this package may import the sqlc-generated tokendb subpackage.
//
// Token wire format: hgx_<prefix>_<secret>
//   - prefix: 8 chars, alphabet [A-Za-z0-9] (~47 bits, used as the public
//     lookup key).
//   - secret: 32 chars, same alphabet (~190 bits). Stored only as a bcrypt
//     hash; never recoverable.
package infra

import (
	"context"
	"crypto/rand"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/infra/tokendb"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// alphabet is the printable charset used for both the public prefix and the
// secret. [A-Za-z0-9] keeps the wire token URL-safe and easy to type/copy.
const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// tokenRegex enforces the exact wire format hgx_<8>_<32>. The handler/user
// layer ultimately produces these but ValidateToken re-checks because the
// plaintext arrives from the network as an opaque string.
var tokenRegex = regexp.MustCompile(`^hgx_[A-Za-z0-9]{8}_[A-Za-z0-9]{32}$`)

// ErrInvalidName is returned by Create when the supplied label is empty or
// longer than 64 chars after trim. Kept package-local so handlers can
// errors.Is it; not part of the frozen domain contract because the contract
// intentionally leaves input validation to the impl.
var ErrInvalidName = errors.New("invalid token name")

// ErrInvalidScope mirrors ErrInvalidName for scope validation; surfaces as a
// 400 in handlers.
var ErrInvalidScope = errors.New("invalid token scope")

// PostgresStore implements domain.Store AND domain.Validator. A single struct
// satisfies both interfaces — the module binds the same instance to both so
// callers can ask for either narrow interface without holding the wider one.
type PostgresStore struct {
	q     *tokendb.Queries
	users userdomain.Repo
}

type PostgresStoreDeps struct {
	Pool  *pgxpool.Pool
	Users userdomain.Repo
}

func NewPostgresStore(deps *PostgresStoreDeps) *PostgresStore {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("token migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_token", "."); err != nil {
		panic(fmt.Errorf("apply token migrations: %w", err))
	}
	return &PostgresStore{
		q:     tokendb.New(deps.Pool),
		users: deps.Users,
	}
}

// Create generates a fresh prefix+secret, bcrypts the secret, inserts the
// row, and returns the row alongside the plaintext (the only moment the
// plaintext exists). Prefix collisions are astronomically unlikely at 62^8
// but we still retry up to 3x on the prefix unique-constraint to be safe.
func (s *PostgresStore) Create(ctx context.Context, userID int64, name string, scopes []domain.Scope, expiresAt *time.Time) (*domain.CreatedToken, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return nil, ErrInvalidName
	}
	for _, sc := range scopes {
		if !sc.Valid() {
			return nil, fmt.Errorf("%w: %q", ErrInvalidScope, sc)
		}
	}

	secret, err := randString(domain.SecretLen)
	if err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash secret: %w", err)
	}

	scopeStrs := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		scopeStrs = append(scopeStrs, string(sc))
	}

	var expiresArg pgtype.Timestamptz
	if expiresAt != nil {
		expiresArg = pgtype.Timestamptz{Time: *expiresAt, Valid: true}
	}

	var row tokendb.AccessToken
	var prefix string
	for range 3 {
		prefix, err = randString(domain.PrefixLen)
		if err != nil {
			return nil, fmt.Errorf("generate prefix: %w", err)
		}
		row, err = s.q.CreateToken(ctx, tokendb.CreateTokenParams{
			UserID:    userID,
			Name:      name,
			Prefix:    prefix,
			HashedKey: string(hashed),
			Scopes:    scopeStrs,
			ExpiresAt: expiresArg,
		})
		if err == nil {
			break
		}
		if !isPrefixConflict(err) {
			return nil, err
		}
		// retry on collision
	}
	if err != nil {
		return nil, fmt.Errorf("create token after retries: %w", err)
	}

	plaintext := "hgx_" + prefix + "_" + secret
	return &domain.CreatedToken{
		Token:     rowToToken(row),
		Plaintext: plaintext,
	}, nil
}

func (s *PostgresStore) ListByUser(ctx context.Context, userID int64) ([]*domain.Token, error) {
	rows, err := s.q.ListTokensByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Token, 0, len(rows))
	for i := range rows {
		out = append(out, rowToToken(rows[i]))
	}
	return out, nil
}

func (s *PostgresStore) Revoke(ctx context.Context, id, userID int64) error {
	n, err := s.q.RevokeToken(ctx, tokendb.RevokeTokenParams{ID: id, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrTokenNotFound
	}
	return nil
}

func (s *PostgresStore) GetByPrefix(ctx context.Context, prefix string) (*domain.Token, error) {
	row, err := s.q.GetTokenByPrefix(ctx, prefix)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrTokenNotFound
		}
		return nil, err
	}
	return rowToToken(row), nil
}

func (s *PostgresStore) TouchLastUsed(ctx context.Context, id int64) error {
	return s.q.TouchTokenLastUsed(ctx, id)
}

// ValidateToken parses the wire token, locates the row by prefix, bcrypt-
// compares the secret, checks active/expiry/revoke, loads the bearer user,
// and best-effort bumps last_used_at. Failure modes map to the domain Err*
// sentinels: format issues → ErrTokenInvalid, missing row → ErrTokenNotFound,
// inactive → ErrTokenRevoked / ErrTokenExpired, disabled user → ErrTokenRevoked.
func (s *PostgresStore) ValidateToken(ctx context.Context, plaintext string) (*domain.Token, *userdomain.User, error) {
	if !tokenRegex.MatchString(plaintext) {
		return nil, nil, domain.ErrTokenInvalid
	}
	parts := strings.Split(plaintext, "_")
	if len(parts) != 3 || parts[0] != "hgx" {
		return nil, nil, domain.ErrTokenInvalid
	}
	prefix, secret := parts[1], parts[2]

	tok, err := s.GetByPrefix(ctx, prefix)
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	if tok.RevokedAt != nil {
		return nil, nil, domain.ErrTokenRevoked
	}
	if tok.ExpiresAt != nil && now.After(*tok.ExpiresAt) {
		return nil, nil, domain.ErrTokenExpired
	}

	if err := bcrypt.CompareHashAndPassword([]byte(tok.HashedKey), []byte(secret)); err != nil {
		return nil, nil, domain.ErrTokenInvalid
	}

	user, err := s.users.GetByID(ctx, tok.UserID)
	if err != nil {
		if errors.Is(err, userdomain.ErrUserNotFound) {
			// orphaned token (FK should prevent this but be defensive)
			return nil, nil, domain.ErrTokenNotFound
		}
		return nil, nil, err
	}
	if user.Disabled {
		return nil, nil, domain.ErrTokenRevoked
	}

	_ = s.TouchLastUsed(ctx, tok.ID)
	return tok, user, nil
}

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

// randString draws n bytes from crypto/rand and maps each into the alphabet.
// 62 doesn't divide 256 evenly so the distribution is *very* slightly biased
// toward the first 256%62=8 chars. For 8- and 32-char tokens that's well
// below the bcrypt+collision-retry threshold of risk; reject-and-resample is
// not worth the complexity.
func randString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

// isPrefixConflict reports whether err is a Postgres unique-violation on the
// access_tokens.prefix column. Used to drive the create-time retry loop.
func isPrefixConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != pgerrcode.UniqueViolation {
		return false
	}
	// constraint name may vary by Postgres version; match by column name
	// embedded in the constraint name as a safety net.
	return strings.Contains(pgErr.ConstraintName, "prefix") ||
		strings.Contains(pgErr.Detail, "prefix")
}
