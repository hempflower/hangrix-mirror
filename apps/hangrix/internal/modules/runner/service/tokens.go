// Package service hosts the runner module's business logic that sits
// between handlers and persistence. Every token here composes Repo
// lookups / writes with cryptography (bcrypt) and wire-format checks
// (regex) — none of those belong in the persistence layer.
//
// Three token surfaces ship:
//
//   - AgentTokenValidator authenticates a runner machine's long-lived
//     `hgxr_<...>` bearer (read path).
//   - SessionTokenValidator authenticates an in-container agent's
//     `hgxs_<...>` bearer (read path).
//   - Enroller (in enrollment.go) trades a one-shot `hgxe_<...>`
//     plaintext for a freshly minted agent token. Redemption is a
//     write under a row-lock, so it goes through Repo via a callback
//     pattern — bcrypt still happens here in service, the transaction
//     stays in infra.
//
// Token minting helpers (`MintAgentToken` / `MintSessionToken` /
// `MintEnrollToken`) live alongside in mint.go.
package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// agentTokenRegex / sessionTokenRegex pin the exact wire format so a
// malformed header is rejected without a database round-trip.
var (
	agentTokenRegex   = regexp.MustCompile(`^hgxr_[A-Za-z0-9]{8}_[A-Za-z0-9]{32}$`)
	sessionTokenRegex = regexp.MustCompile(`^hgxs_[A-Za-z0-9]{8}_[A-Za-z0-9]{32}$`)
)

// agentTokenLookup is the narrow read the AgentTokenValidator needs.
// Declared here (not in domain/) because no other package consumes it —
// it exists only to keep the validator decoupled from the wide Repo.
type agentTokenLookup interface {
	GetRunnerByAgentTokenPrefix(ctx context.Context, prefix string) (*domain.Runner, error)
}

// sessionTokenLookup mirrors agentTokenLookup for the session validator.
type sessionTokenLookup interface {
	GetSessionByTokenPrefix(ctx context.Context, prefix string) (*domain.AgentSession, error)
}

// AgentTokenValidator implements domain.AgentValidator.
//
// The lookup is satisfied by the runner module's Repo without exposing
// the rest of its surface; future implementations can swap in a cache
// without touching the validator.
type AgentTokenValidator struct {
	lookup agentTokenLookup
}

// AgentTokenValidatorDeps is the ioc-shaped constructor input. Asks for
// the full Repo (which obviously satisfies agentTokenLookup) so the
// container can wire the singleton Postgres impl in via its widest type.
type AgentTokenValidatorDeps struct {
	Repo domain.Repo
}

func NewAgentTokenValidator(deps *AgentTokenValidatorDeps) *AgentTokenValidator {
	return &AgentTokenValidator{lookup: deps.Repo}
}

// ValidateAgentToken parses an `hgxr_<...>` plaintext, looks up the row
// by prefix, bcrypt-compares, and asserts AgentTokenActive. Wrong shape
// is ErrInvalidToken (not "not found") so a malformed header doesn't
// leak the existence of a prefix.
func (v *AgentTokenValidator) ValidateAgentToken(ctx context.Context, plaintext string) (*domain.Runner, error) {
	if !agentTokenRegex.MatchString(plaintext) {
		return nil, domain.ErrInvalidToken
	}
	rest := strings.TrimPrefix(plaintext, domain.AgentTokenWirePrefix)
	parts := strings.Split(rest, "_")
	if len(parts) != 2 {
		return nil, domain.ErrInvalidToken
	}
	prefix, secret := parts[0], parts[1]

	rr, err := v.lookup.GetRunnerByAgentTokenPrefix(ctx, prefix)
	if err != nil {
		if errors.Is(err, domain.ErrRunnerNotFound) {
			return nil, domain.ErrInvalidToken
		}
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(rr.AgentTokenHash), []byte(secret)); err != nil {
		return nil, domain.ErrInvalidToken
	}
	if !rr.AgentTokenActive() {
		return nil, domain.ErrTokenInactive
	}
	return rr, nil
}

// SessionTokenValidator implements domain.SessionTokenValidator.
type SessionTokenValidator struct {
	lookup sessionTokenLookup
}

type SessionTokenValidatorDeps struct {
	Repo domain.Repo
}

func NewSessionTokenValidator(deps *SessionTokenValidatorDeps) *SessionTokenValidator {
	return &SessionTokenValidator{lookup: deps.Repo}
}

// ValidateSessionToken parses an `hgxs_<...>` plaintext, looks up the
// owning agent_sessions row, bcrypt-compares, and asserts the session
// is still active (not terminal, not explicitly revoked).
func (v *SessionTokenValidator) ValidateSessionToken(ctx context.Context, plaintext string) (*domain.AgentSession, error) {
	if !sessionTokenRegex.MatchString(plaintext) {
		return nil, domain.ErrInvalidSessionToken
	}
	rest := strings.TrimPrefix(plaintext, domain.SessionTokenWirePrefix)
	parts := strings.Split(rest, "_")
	if len(parts) != 2 {
		return nil, domain.ErrInvalidSessionToken
	}
	prefix, secret := parts[0], parts[1]

	s, err := v.lookup.GetSessionByTokenPrefix(ctx, prefix)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			return nil, domain.ErrInvalidSessionToken
		}
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(s.SessionTokenHash), []byte(secret)); err != nil {
		return nil, domain.ErrInvalidSessionToken
	}
	if !s.SessionTokenActive(time.Now()) {
		return nil, domain.ErrSessionTokenInactive
	}
	return s, nil
}
