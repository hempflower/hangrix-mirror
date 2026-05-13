// Package domain declares the Personal Access Token (PAT) types and the
// interfaces that other modules import: Store (CRUD against the backing DB)
// and Validator (plaintext → user lookup, used by the smart-HTTP layer to
// authenticate git push without dragging a circular dep through auth).
//
// Token wire format (returned ONCE on creation, never reconstructable
// server-side):
//
//	hgx_<prefix>_<secret>
//
// Where <prefix> is a public 8-char identifier stored alongside a bcrypt
// hash of <secret>. Lookup goes prefix → row → bcrypt-compare. The prefix
// makes validation O(1) without scanning every hashed row.
package domain

import (
	"context"
	"errors"
	"time"

	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

// Scope is the coarse permission a token bears. M3 intentionally ships only
// two — read vs write at repo granularity. Finer breakdowns (issue:write,
// admin:*, etc.) wait until they're actually needed.
type Scope string

const (
	ScopeRepoRead  Scope = "repo:read"
	ScopeRepoWrite Scope = "repo:write"
)

func (s Scope) Valid() bool { return s == ScopeRepoRead || s == ScopeRepoWrite }

// PrefixLen is the public-prefix length built into every PAT's wire format.
// Bumping this is a wire-format change.
const PrefixLen = 8

// SecretLen is the entropy portion length (chars). 32 chars of base32
// alphabet = ~160 bits.
const SecretLen = 32

// Token is the persisted record. The plaintext secret is never stored.
type Token struct {
	ID         int64
	UserID     int64
	Name       string // human-friendly label set by the owner
	Prefix     string // 8-char public identifier; indexed
	HashedKey  string // bcrypt(secret)
	Scopes     []Scope
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// HasScope reports whether the token bears the given scope. ScopeRepoWrite
// implies ScopeRepoRead for read-side checks.
func (t *Token) HasScope(s Scope) bool {
	for _, x := range t.Scopes {
		if x == s {
			return true
		}
		// write implies read
		if s == ScopeRepoRead && x == ScopeRepoWrite {
			return true
		}
	}
	return false
}

// Active reports whether the token can still be used (not revoked, not expired).
func (t *Token) Active(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	if t.ExpiresAt != nil && now.After(*t.ExpiresAt) {
		return false
	}
	return true
}

// CreatedToken is returned by Store.Create. Plaintext is the wire-format
// token (`hgx_<prefix>_<secret>`) shown to the user exactly once.
type CreatedToken struct {
	Token     *Token
	Plaintext string
}

type Store interface {
	// Create generates a fresh secret, persists the hash with prefix +
	// scopes + expiry, and returns the row + plaintext.
	Create(ctx context.Context, userID int64, name string, scopes []Scope, expiresAt *time.Time) (*CreatedToken, error)

	// ListByUser returns all tokens belonging to userID, including revoked
	// ones (the UI may want to show revoked history). Caller filters.
	ListByUser(ctx context.Context, userID int64) ([]*Token, error)

	// Revoke marks the token revoked. Returns ErrTokenNotFound if id
	// doesn't exist or doesn't belong to userID.
	Revoke(ctx context.Context, id, userID int64) error

	// GetByPrefix loads the token row for the given public prefix. Used
	// by Validator; not exposed to handlers.
	GetByPrefix(ctx context.Context, prefix string) (*Token, error)

	// TouchLastUsed bumps last_used_at to NOW(). Best-effort; ignore
	// errors at call sites.
	TouchLastUsed(ctx context.Context, id int64) error
}

// Validator resolves a plaintext PAT to its bearer user. It's a separate
// interface so the smart-HTTP layer can depend on a narrow surface without
// pulling in the rest of Store.
type Validator interface {
	// ValidateToken parses the plaintext, looks up the row by prefix,
	// bcrypt-compares, checks active state, and returns the token + user.
	// On any failure returns one of the Err* sentinels.
	ValidateToken(ctx context.Context, plaintext string) (*Token, *userdomain.User, error)
}

var (
	ErrTokenNotFound = errors.New("token not found")
	ErrTokenInvalid  = errors.New("token invalid format")
	ErrTokenExpired  = errors.New("token expired")
	ErrTokenRevoked  = errors.New("token revoked")
)
