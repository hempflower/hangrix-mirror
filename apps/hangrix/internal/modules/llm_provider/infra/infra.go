// Package infra holds the Postgres-backed implementation of the
// llm_provider domain. Queries are written inline with pgx — sqlc is not
// used here because the surface is small and the api_key_encrypted column
// is a transformed value (sealed cryptobox blob) that we'd otherwise have
// to special-case in every generated method anyway.
//
// Encryption contract:
//   - CreateProvider / UpdateProvider treat the inbound Provider.ApiKey as
//     plaintext. Both methods seal it via the cryptobox before insert /
//     update. An empty ApiKey on UpdateProvider means "leave the stored
//     key unchanged".
//   - GetProviderByName / GetProviderByID / ListProviders return the
//     SEALED blob in Provider.ApiKey. It is the caller's job (the
//     llm_proxy) to decrypt; handlers in this module never surface the
//     field on the wire.
//
// Session-token wire format mirrors the PAT format used by modules/token,
// only the prefix differs: hgxs_<8>_<32>, alphabet [A-Za-z0-9]. Stored as
// (public prefix, bcrypt(secret)) so the secret is non-recoverable.
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

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
	"github.com/hangrix/hangrix/pkg/cryptobox"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// alphabet is the printable charset used for both the public prefix and the
// secret. [A-Za-z0-9] keeps the wire token URL-safe and easy to copy.
const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// sessionTokenRegex enforces the exact wire format hgxs_<8>_<32>. Used by
// ValidateToken; the underscores split out a fixed-length prefix and secret.
var sessionTokenRegex = regexp.MustCompile(`^hgxs_[A-Za-z0-9]{8}_[A-Za-z0-9]{32}$`)

// PostgresRepo implements domain.Repo, domain.Lookup, and domain.Validator
// on a single pgx pool. Splitting these would duplicate state; the module
// binds the same instance to all three interfaces.
type PostgresRepo struct {
	pool *pgxpool.Pool
	box  *cryptobox.Box
}

type PostgresRepoDeps struct {
	Pool   *pgxpool.Pool
	Config *config.Config
}

// NewPostgresRepo applies migrations and constructs the cryptobox up front
// so a malformed master key fails loudly at startup instead of on the first
// CreateProvider call. Mirrors the fail-loud pattern of the other modules'
// migration runners.
func NewPostgresRepo(deps *PostgresRepoDeps) *PostgresRepo {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("llm_provider migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_llm_provider", "."); err != nil {
		panic(fmt.Errorf("apply llm_provider migrations: %w", err))
	}
	box, err := cryptobox.New(deps.Config.LLM.EncryptionKey)
	if err != nil {
		panic(fmt.Errorf("llm_provider cryptobox: %w", err))
	}
	return &PostgresRepo{pool: deps.Pool, box: box}
}

// ---- providers ----

// CreateProvider seals p.ApiKey via the cryptobox before insert. The caller
// is expected to pass plaintext; the on-disk value is the sealed blob and
// the returned Provider.ApiKey is the sealed blob (so downstream callers
// like the proxy can decrypt without a second round-trip).
func (r *PostgresRepo) CreateProvider(ctx context.Context, p *domain.Provider) (*domain.Provider, error) {
	sealed, err := r.box.Encrypt(p.ApiKey)
	if err != nil {
		return nil, fmt.Errorf("seal api key: %w", err)
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO llm_providers (
			name, type, base_url, api_key_encrypted, allowed_models,
			visibility, allowed_repos, rate_limit_rpm,
			is_platform_default, default_model, created_by
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, name, type, base_url, api_key_encrypted, allowed_models,
		          visibility, allowed_repos, rate_limit_rpm,
		          is_platform_default, default_model, created_by,
		          created_at, updated_at
	`,
		p.Name, string(p.Type), p.BaseURL, sealed, p.AllowedModels,
		string(p.Visibility), p.AllowedRepos, p.RateLimitRPM,
		p.IsPlatformDefault, p.DefaultModel, p.CreatedBy,
	)
	out, err := scanProvider(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrProviderConflict
		}
		return nil, err
	}
	return out, nil
}

// UpdateProvider re-seals p.ApiKey when non-empty; an empty value leaves the
// stored key unchanged. Type and name are immutable through this path —
// callers must delete and recreate to switch them.
func (r *PostgresRepo) UpdateProvider(ctx context.Context, p *domain.Provider) (*domain.Provider, error) {
	var sealed string
	if p.ApiKey != "" {
		s, err := r.box.Encrypt(p.ApiKey)
		if err != nil {
			return nil, fmt.Errorf("seal api key: %w", err)
		}
		sealed = s
	}
	// COALESCE keeps the stored key when the caller passed empty. The empty
	// string is encoded as NULL via pgtype so the conditional branches in
	// CASE/COALESCE land predictably.
	var keyArg pgtype.Text
	if sealed != "" {
		keyArg = pgtype.Text{String: sealed, Valid: true}
	}
	row := r.pool.QueryRow(ctx, `
		UPDATE llm_providers SET
			base_url            = $2,
			api_key_encrypted   = COALESCE($3, api_key_encrypted),
			allowed_models      = $4,
			visibility          = $5,
			allowed_repos       = $6,
			rate_limit_rpm      = $7,
			is_platform_default = $8,
			default_model       = $9,
			updated_at          = NOW()
		WHERE id = $1
		RETURNING id, name, type, base_url, api_key_encrypted, allowed_models,
		          visibility, allowed_repos, rate_limit_rpm,
		          is_platform_default, default_model, created_by,
		          created_at, updated_at
	`,
		p.ID, p.BaseURL, keyArg, p.AllowedModels,
		string(p.Visibility), p.AllowedRepos, p.RateLimitRPM,
		p.IsPlatformDefault, p.DefaultModel,
	)
	out, err := scanProvider(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrProviderNotFound
		}
		return nil, err
	}
	return out, nil
}

func (r *PostgresRepo) GetProviderByName(ctx context.Context, name string) (*domain.Provider, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, type, base_url, api_key_encrypted, allowed_models,
		       visibility, allowed_repos, rate_limit_rpm,
		       is_platform_default, default_model, created_by,
		       created_at, updated_at
		FROM llm_providers WHERE name = $1
	`, name)
	p, err := scanProvider(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrProviderNotFound
		}
		return nil, err
	}
	return p, nil
}

func (r *PostgresRepo) GetProviderByID(ctx context.Context, id int64) (*domain.Provider, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, type, base_url, api_key_encrypted, allowed_models,
		       visibility, allowed_repos, rate_limit_rpm,
		       is_platform_default, default_model, created_by,
		       created_at, updated_at
		FROM llm_providers WHERE id = $1
	`, id)
	p, err := scanProvider(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrProviderNotFound
		}
		return nil, err
	}
	return p, nil
}

func (r *PostgresRepo) ListProviders(ctx context.Context) ([]*domain.Provider, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, type, base_url, api_key_encrypted, allowed_models,
		       visibility, allowed_repos, rate_limit_rpm,
		       is_platform_default, default_model, created_by,
		       created_at, updated_at
		FROM llm_providers
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*domain.Provider{}
	for rows.Next() {
		p, err := scanProviderRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *PostgresRepo) DeleteProvider(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM llm_providers WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrProviderNotFound
	}
	return nil
}

// SetPlatformDefault wraps the clear-then-set in a single transaction so a
// concurrent caller cannot momentarily observe two defaults (or zero) — the
// partial unique index would also reject any in-between state, but the
// transaction avoids a spurious failure when both UPDATEs race.
func (r *PostgresRepo) SetPlatformDefault(ctx context.Context, id int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE llm_providers SET is_platform_default = FALSE, updated_at = NOW() WHERE id <> $1 AND is_platform_default = TRUE`,
		id,
	); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE llm_providers SET is_platform_default = TRUE, updated_at = NOW() WHERE id = $1`,
		id,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrProviderNotFound
	}
	return tx.Commit(ctx)
}

func (r *PostgresRepo) GetPlatformDefault(ctx context.Context) (*domain.Provider, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, type, base_url, api_key_encrypted, allowed_models,
		       visibility, allowed_repos, rate_limit_rpm,
		       is_platform_default, default_model, created_by,
		       created_at, updated_at
		FROM llm_providers
		WHERE is_platform_default = TRUE
		LIMIT 1
	`)
	p, err := scanProvider(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrProviderNotFound
		}
		return nil, err
	}
	return p, nil
}

// ---- session tokens ----

// CreateSessionToken mints a fresh prefix+secret pair, bcrypts the secret,
// and returns the row plus the wire-format plaintext (the only moment the
// plaintext exists). Prefix collisions are astronomically unlikely but the
// caller retries up to 3x on the prefix unique-constraint for robustness.
func (r *PostgresRepo) CreateSessionToken(ctx context.Context, providerID int64, model, label string, createdBy int64, expiresAt *time.Time) (*domain.CreatedSessionToken, error) {
	secret, err := randString(domain.SessionTokenSecretLen)
	if err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash secret: %w", err)
	}

	var expiresArg pgtype.Timestamptz
	if expiresAt != nil {
		expiresArg = pgtype.Timestamptz{Time: *expiresAt, Valid: true}
	}

	var (
		id     int64
		prefix string
		stored = struct {
			createdAt pgtype.Timestamptz
		}{}
	)
	var lastErr error
	for range 3 {
		prefix, err = randString(domain.SessionTokenPrefixLen)
		if err != nil {
			return nil, fmt.Errorf("generate prefix: %w", err)
		}
		row := r.pool.QueryRow(ctx, `
			INSERT INTO llm_session_tokens (prefix, hashed_key, provider_id, model, label, created_by, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id, created_at
		`, prefix, string(hashed), providerID, model, label, createdBy, expiresArg)
		if err := row.Scan(&id, &stored.createdAt); err != nil {
			if isPrefixConflict(err) {
				lastErr = err
				continue
			}
			return nil, err
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return nil, fmt.Errorf("create session token after retries: %w", lastErr)
	}

	tok := &domain.SessionToken{
		ID:         id,
		Prefix:     prefix,
		HashedKey:  string(hashed),
		ProviderID: providerID,
		Model:      model,
		Label:      label,
		CreatedBy:  createdBy,
		CreatedAt:  stored.createdAt.Time,
	}
	if expiresAt != nil {
		v := *expiresAt
		tok.ExpiresAt = &v
	}
	plaintext := domain.SessionTokenWirePrefix + prefix + "_" + secret
	return &domain.CreatedSessionToken{Token: tok, Plaintext: plaintext}, nil
}

func (r *PostgresRepo) GetSessionTokenByPrefix(ctx context.Context, prefix string) (*domain.SessionToken, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, prefix, hashed_key, provider_id, model, label, created_by,
		       last_used_at, expires_at, revoked_at, created_at
		FROM llm_session_tokens WHERE prefix = $1
	`, prefix)
	t, err := scanSessionToken(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrTokenNotFound
		}
		return nil, err
	}
	return t, nil
}

func (r *PostgresRepo) ListSessionTokens(ctx context.Context) ([]*domain.SessionToken, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, prefix, hashed_key, provider_id, model, label, created_by,
		       last_used_at, expires_at, revoked_at, created_at
		FROM llm_session_tokens
		ORDER BY id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*domain.SessionToken{}
	for rows.Next() {
		t, err := scanSessionTokenRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *PostgresRepo) RevokeSessionToken(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE llm_session_tokens SET revoked_at = NOW() WHERE id = $1 AND revoked_at IS NULL`,
		id,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// either it does not exist or it was already revoked; check which.
		var exists bool
		if err := r.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM llm_session_tokens WHERE id = $1)`, id,
		).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return domain.ErrTokenNotFound
		}
	}
	return nil
}

// TouchSessionTokenLastUsed is best-effort: callers (the proxy) invoke this
// after a successful upstream call and ignore errors. The row may have been
// revoked in the meantime; that is fine — last_used_at on a revoked token is
// still informative for audit.
func (r *PostgresRepo) TouchSessionTokenLastUsed(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE llm_session_tokens SET last_used_at = NOW() WHERE id = $1`,
		id,
	)
	return err
}

// ---- usage log ----

func (r *PostgresRepo) RecordUsage(ctx context.Context, u *domain.UsageRecord) error {
	var sessionArg pgtype.Int8
	if u.SessionTokenID != nil {
		sessionArg = pgtype.Int8{Int64: *u.SessionTokenID, Valid: true}
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO llm_usage_log (
			session_token_id, provider_id, model,
			prompt_tokens, completion_tokens, total_tokens,
			latency_ms, status_code, error_message, request_path
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		sessionArg, u.ProviderID, u.Model,
		u.PromptTokens, u.CompletionTokens, u.TotalTokens,
		u.LatencyMS, u.StatusCode, u.ErrorMessage, u.RequestPath,
	)
	return err
}

// ListUsage is a read helper outside the Repo interface — the admin handler
// renders it; the proxy never calls it. Kept here so all SQL for the module
// stays in one file. since/limit are clamped by the caller.
func (r *PostgresRepo) ListUsage(ctx context.Context, providerID *int64, since *time.Time, limit int) ([]*UsageRow, error) {
	args := []any{}
	where := []string{}
	if providerID != nil {
		args = append(args, *providerID)
		where = append(where, fmt.Sprintf("u.provider_id = $%d", len(args)))
	}
	if since != nil {
		args = append(args, *since)
		where = append(where, fmt.Sprintf("u.created_at >= $%d", len(args)))
	}
	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	sqlText := fmt.Sprintf(`
		SELECT u.id, u.session_token_id, u.provider_id, p.name,
		       u.model, u.prompt_tokens, u.completion_tokens, u.total_tokens,
		       u.latency_ms, u.status_code, u.error_message, u.request_path,
		       u.created_at
		FROM llm_usage_log u
		JOIN llm_providers p ON p.id = u.provider_id
		%s
		ORDER BY u.created_at DESC
		LIMIT $%d
	`, clause, len(args))

	rows, err := r.pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*UsageRow{}
	for rows.Next() {
		var (
			ur          UsageRow
			sessionID   pgtype.Int8
			createdAt   pgtype.Timestamptz
		)
		if err := rows.Scan(
			&ur.ID, &sessionID, &ur.ProviderID, &ur.ProviderName,
			&ur.Model, &ur.PromptTokens, &ur.CompletionTokens, &ur.TotalTokens,
			&ur.LatencyMS, &ur.StatusCode, &ur.ErrorMessage, &ur.RequestPath,
			&createdAt,
		); err != nil {
			return nil, err
		}
		if sessionID.Valid {
			v := sessionID.Int64
			ur.SessionTokenID = &v
		}
		ur.CreatedAt = createdAt.Time
		out = append(out, &ur)
	}
	return out, rows.Err()
}

// UsageRow is the read-side projection returned by ListUsage. It mirrors
// domain.UsageRecord plus a denormalized provider name for display and the
// row id/timestamp the writer never sees.
type UsageRow struct {
	ID               int64
	SessionTokenID   *int64
	ProviderID       int64
	ProviderName     string
	Model            string
	PromptTokens     int32
	CompletionTokens int32
	TotalTokens      int32
	LatencyMS        int32
	StatusCode       int32
	ErrorMessage     string
	RequestPath      string
	CreatedAt        time.Time
}

// ---- validator ----

// ValidateToken implements the Validator contract used by the proxy. The
// flow is: shape check (cheap, avoids DB hit on garbage input) → split into
// prefix/secret → row lookup by prefix → bcrypt compare → active check →
// load owning provider. Errors map to ErrInvalidToken (bad shape / mismatch),
// ErrTokenNotFound (no such prefix), ErrTokenInactive (revoked or expired).
func (r *PostgresRepo) ValidateToken(ctx context.Context, plaintext string) (*domain.Validated, error) {
	if !sessionTokenRegex.MatchString(plaintext) {
		return nil, domain.ErrInvalidToken
	}
	// Strip the leading "hgxs_" then split prefix_secret. Doing it on the
	// trimmed remainder avoids an extra "hgxs" element from Split.
	rest := strings.TrimPrefix(plaintext, domain.SessionTokenWirePrefix)
	parts := strings.Split(rest, "_")
	if len(parts) != 2 {
		return nil, domain.ErrInvalidToken
	}
	prefix, secret := parts[0], parts[1]

	tok, err := r.GetSessionTokenByPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(tok.HashedKey), []byte(secret)); err != nil {
		return nil, domain.ErrInvalidToken
	}
	if !tok.Active(time.Now()) {
		return nil, domain.ErrTokenInactive
	}
	prov, err := r.GetProviderByID(ctx, tok.ProviderID)
	if err != nil {
		// Provider deleted out from under the token: FK CASCADE should
		// prevent this, but treat it as an inactive token if it ever
		// happens rather than leaking a 500.
		if errors.Is(err, domain.ErrProviderNotFound) {
			return nil, domain.ErrTokenInactive
		}
		return nil, err
	}
	return &domain.Validated{Token: tok, Provider: prov}, nil
}

// ---- scan helpers ----

// scanner unifies pgx.Row and pgx.Rows so a single helper covers both
// QueryRow and Query loops.
type scanner interface {
	Scan(dest ...any) error
}

func scanProvider(r pgx.Row) (*domain.Provider, error) {
	return scanProviderRow(r)
}

func scanProviderRow(r scanner) (*domain.Provider, error) {
	var (
		p           domain.Provider
		typeStr     string
		visStr      string
		createdAt   pgtype.Timestamptz
		updatedAt   pgtype.Timestamptz
	)
	if err := r.Scan(
		&p.ID, &p.Name, &typeStr, &p.BaseURL, &p.ApiKey, &p.AllowedModels,
		&visStr, &p.AllowedRepos, &p.RateLimitRPM,
		&p.IsPlatformDefault, &p.DefaultModel, &p.CreatedBy,
		&createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	p.Type = domain.ProviderType(typeStr)
	p.Visibility = domain.Visibility(visStr)
	p.CreatedAt = createdAt.Time
	p.UpdatedAt = updatedAt.Time
	return &p, nil
}

func scanSessionToken(r pgx.Row) (*domain.SessionToken, error) {
	return scanSessionTokenRow(r)
}

func scanSessionTokenRow(r scanner) (*domain.SessionToken, error) {
	var (
		t          domain.SessionToken
		lastUsed   pgtype.Timestamptz
		expiresAt  pgtype.Timestamptz
		revokedAt  pgtype.Timestamptz
		createdAt  pgtype.Timestamptz
	)
	if err := r.Scan(
		&t.ID, &t.Prefix, &t.HashedKey, &t.ProviderID, &t.Model, &t.Label, &t.CreatedBy,
		&lastUsed, &expiresAt, &revokedAt, &createdAt,
	); err != nil {
		return nil, err
	}
	if lastUsed.Valid {
		v := lastUsed.Time
		t.LastUsedAt = &v
	}
	if expiresAt.Valid {
		v := expiresAt.Time
		t.ExpiresAt = &v
	}
	if revokedAt.Valid {
		v := revokedAt.Time
		t.RevokedAt = &v
	}
	t.CreatedAt = createdAt.Time
	return &t, nil
}

// ---- pgx error helpers ----

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation
}

// isPrefixConflict reports whether err is a unique-violation specifically on
// the session-token prefix column. Used by the create-time retry loop.
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

// randString draws n bytes from crypto/rand and maps each byte into the
// alphabet. The 62-char alphabet doesn't evenly divide 256 so the
// distribution is *very* slightly biased toward the first 8 chars; for
// 8/32-char tokens this remains well below the bcrypt+collision-retry risk
// threshold, so we keep the simple modulo mapping.
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
