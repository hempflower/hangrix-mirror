// Package domain declares the Personal Access Token (PAT) types and the
// interfaces that other modules import.
//
// Two interfaces, each scoped to a different consumer:
//   - Store — what HTTP handlers see (Create / List / Revoke). Implemented
//     by the service layer, which adds validation + secret minting on top
//     of the narrower persistence layer.
//   - Validator — what the smart-HTTP layer holds to authenticate
//     plaintext PATs without dragging in the rest of Store.
//
// Persistence sits behind Repo (declared here, implemented by infra).
// Service composes Repo with bcrypt + regex to satisfy Store and
// Validator; handlers never see Repo.
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

// WirePrefix is the literal prefix every PAT plaintext begins with.
// Distinct from session tokens (`hgxs_`) and runner tokens (`hgxe_` /
// `hgxr_`) so a single auth router can dispatch by inspecting the
// header alone.
const WirePrefix = "hgx_"

// PrefixLen is the public-prefix length built into every PAT's wire format.
// Bumping this is a wire-format change.
const PrefixLen = 8

// SecretLen is the entropy portion length (chars). 32 chars of base32
// alphabet = ~160 bits.
const SecretLen = 32

// MaxNameLen caps the user-supplied label length. Anything longer is
// rejected at the validation boundary — keeps the column unbounded
// neither in DB nor in display lists.
const MaxNameLen = 64

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

// InsertParams is the bag the service hands to Repo.Insert. The fields
// have already been validated + the secret bcrypted; Repo only writes a
// row. Separating this from the Store.Create signature keeps the
// validation boundary explicit — Repo is incapable of accepting
// unvalidated input.
type InsertParams struct {
	UserID    int64
	Name      string
	Prefix    string
	HashedKey string
	Scopes    []Scope
	ExpiresAt *time.Time
}

// Repo is the persistence-only interface. Implemented by infra. Service
// composes this with bcrypt + regex + minting to satisfy Store /
// Validator. Cross-module callers should depend on Store / Validator,
// not Repo.
type Repo interface {
	// Insert writes a pre-validated, pre-hashed token row. Returns
	// ErrPrefixConflict on a unique-violation specifically over the
	// prefix column so the caller can retry with a fresh prefix.
	Insert(ctx context.Context, params InsertParams) (*Token, error)

	ListByUser(ctx context.Context, userID int64) ([]*Token, error)
	Revoke(ctx context.Context, id, userID int64) error
	GetByPrefix(ctx context.Context, prefix string) (*Token, error)
	TouchLastUsed(ctx context.Context, id int64) error
}

// Store is the handler-facing interface. Adds validation + secret
// minting on top of Repo.
type Store interface {
	// Create validates name + scopes, mints a fresh secret, bcrypts it,
	// and persists. Returns the row + the wire-format plaintext shown
	// to the user exactly once.
	Create(ctx context.Context, userID int64, name string, scopes []Scope, expiresAt *time.Time) (*CreatedToken, error)

	ListByUser(ctx context.Context, userID int64) ([]*Token, error)
	Revoke(ctx context.Context, id, userID int64) error
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
	ErrTokenNotFound  = errors.New("token not found")
	ErrTokenInvalid   = errors.New("token invalid format")
	ErrTokenExpired   = errors.New("token expired")
	ErrTokenRevoked   = errors.New("token revoked")
	ErrInvalidName    = errors.New("invalid token name")
	ErrInvalidScope   = errors.New("invalid token scope")
	ErrPrefixConflict = errors.New("token prefix conflict")
)
