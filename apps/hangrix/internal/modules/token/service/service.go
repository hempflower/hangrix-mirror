// Package service hosts the stateless business logic for Personal
// Access Tokens. It composes domain.Repo (persistence) with bcrypt
// (crypto) and regex (wire format) to satisfy the broader
// domain.Store / domain.Validator interfaces handlers depend on.
//
// Why a service layer:
//
//   - Persistence (infra) only knows how to insert / read / update
//     rows. It must not run bcrypt, mint secrets, or interpret wire
//     formats — those are orthogonal concerns that change at a
//     different cadence (e.g. swapping bcrypt for argon2id is a
//     service-layer change, not a schema change).
//   - The same composition appears in modules/runner/service for
//     `hgxr_` / `hgxs_` tokens; this file is the parallel for `hgx_`
//     PATs.
package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

// alphabet is shared with session tokens / runner tokens:
// [A-Za-z0-9], URL-safe and easy to paste in a terminal.
const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// tokenRegex enforces the exact wire format hgx_<8>_<32>. Validator
// re-checks even though prefix lookup would also reject malformed
// inputs — failing on shape before a DB round-trip is cheap and keeps
// "garbage input doesn't load the user repository" obvious.
var tokenRegex = regexp.MustCompile(`^hgx_[A-Za-z0-9]{8}_[A-Za-z0-9]{32}$`)

// prefixRetries bounds how many times we re-mint a prefix when the
// underlying INSERT collides. At 62^8 collisions are astronomical;
// three attempts is a defensive ceiling that turns "DB hiccup"
// loops into a clean error.
const prefixRetries = 3

// Service composes Repo + user lookup + bcrypt to implement both
// domain.Store and domain.Validator on one type.
type Service struct {
	repo  domain.Repo
	users userdomain.Repo
}

// Deps is the ioc-shaped input. Asks for the user repo because token
// validation must look up + check disabled-state on the bearer user.
type Deps struct {
	Repo  domain.Repo
	Users userdomain.Repo
}

func New(deps *Deps) *Service {
	return &Service{repo: deps.Repo, users: deps.Users}
}

// ---- Store impl ----

// Create validates the inputs, mints a fresh secret + prefix, bcrypts
// the secret, and writes one token row via Repo.Insert. Prefix
// collisions are retried up to prefixRetries times — the second-line
// of defence on top of 62^8 entropy.
func (s *Service) Create(
	ctx context.Context,
	userID int64,
	name string,
	scopes []domain.Scope,
	expiresAt *time.Time,
) (*domain.CreatedToken, error) {
	n, err := domain.ValidateName(name)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateScopes(scopes); err != nil {
		return nil, err
	}

	secret, err := randString(domain.SecretLen)
	if err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash secret: %w", err)
	}

	var (
		row    *domain.Token
		prefix string
	)
	for attempt := 0; attempt < prefixRetries; attempt++ {
		prefix, err = randString(domain.PrefixLen)
		if err != nil {
			return nil, fmt.Errorf("generate prefix: %w", err)
		}
		row, err = s.repo.Insert(ctx, domain.InsertParams{
			UserID:    userID,
			Name:      n,
			Prefix:    prefix,
			HashedKey: string(hashed),
			Scopes:    scopes,
			ExpiresAt: expiresAt,
		})
		if err == nil {
			break
		}
		if !errors.Is(err, domain.ErrPrefixConflict) {
			return nil, err
		}
	}
	if err != nil {
		return nil, fmt.Errorf("create token after retries: %w", err)
	}

	plaintext := domain.WirePrefix + prefix + "_" + secret
	return &domain.CreatedToken{Token: row, Plaintext: plaintext}, nil
}

// ListByUser and Revoke pass through; no business logic to add.
func (s *Service) ListByUser(ctx context.Context, userID int64) ([]*domain.Token, error) {
	return s.repo.ListByUser(ctx, userID)
}

func (s *Service) Revoke(ctx context.Context, id, userID int64) error {
	return s.repo.Revoke(ctx, id, userID)
}

// ---- Validator impl ----

// ValidateToken parses the plaintext, resolves the row by prefix,
// bcrypt-compares, checks active/expiry/revoke, loads the bearer
// user, and best-effort bumps last_used_at. Failure modes map to
// domain Err* sentinels: format issues → ErrTokenInvalid, missing row
// → ErrTokenNotFound, inactive → ErrTokenRevoked / ErrTokenExpired,
// disabled user → ErrTokenRevoked.
func (s *Service) ValidateToken(ctx context.Context, plaintext string) (*domain.Token, *userdomain.User, error) {
	if !tokenRegex.MatchString(plaintext) {
		return nil, nil, domain.ErrTokenInvalid
	}
	rest := strings.TrimPrefix(plaintext, domain.WirePrefix)
	parts := strings.Split(rest, "_")
	if len(parts) != 2 {
		return nil, nil, domain.ErrTokenInvalid
	}
	prefix, secret := parts[0], parts[1]

	tok, err := s.repo.GetByPrefix(ctx, prefix)
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
			// Orphaned token (FK should prevent this, but defensive).
			return nil, nil, domain.ErrTokenNotFound
		}
		return nil, nil, err
	}
	if user.Disabled {
		return nil, nil, domain.ErrTokenRevoked
	}

	_ = s.repo.TouchLastUsed(ctx, tok.ID)
	return tok, user, nil
}

// ---- helpers ----

// randString draws n bytes from crypto/rand and maps each into the
// alphabet. The 62-char alphabet doesn't evenly divide 256 so the
// distribution is *very* slightly biased toward the first 256%62=8
// chars; for 8- and 32-char tokens that's well below the
// bcrypt+collision-retry risk threshold, so we keep the simple modulo
// mapping. Same trade-off as the runner-token mint.
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
