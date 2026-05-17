package service

import (
	"context"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// enrollTokenRegex pins the exact `hgxe_<8>_<32>` wire format. Wire
// validation lives in service alongside the agent / session validators
// — persistence never sees a malformed input.
var enrollTokenRegex = regexp.MustCompile(`^hgxe_[A-Za-z0-9]{8}_[A-Za-z0-9]{32}$`)

// Enroller implements domain.EnrollValidator on top of the narrow
// Repo.RedeemEnrollment primitive. The callback pattern keeps the
// transaction in infra while bcrypt verification + token minting stay
// here in service.
type Enroller struct {
	repo domain.Repo
}

type EnrollerDeps struct {
	Repo domain.Repo
}

func NewEnroller(deps *EnrollerDeps) *Enroller {
	return &Enroller{repo: deps.Repo}
}

// RedeemEnrollment runs the enrollment exchange:
//
//  1. Validate wire format + split (prefix, secret).
//  2. Mint a fresh agent token plaintext + bcrypt(secret).
//  3. Hand both to Repo.RedeemEnrollment, supplying a verify closure
//     that the repo calls between the row lock and the UPDATE. The
//     closure bcrypt-compares the inbound enrollment secret against
//     the locked row's stored hash.
//
// Failures map to domain sentinels: bad shape → ErrInvalidToken,
// secret mismatch → ErrInvalidToken, runner disabled →
// ErrRunnerDisabled, already redeemed → ErrEnrollUsed.
func (e *Enroller) RedeemEnrollment(
	ctx context.Context,
	plaintext string,
	capabilities []byte,
) (*domain.RedeemEnrollResult, error) {
	if !enrollTokenRegex.MatchString(plaintext) {
		return nil, domain.ErrInvalidToken
	}
	rest := strings.TrimPrefix(plaintext, domain.EnrollTokenWirePrefix)
	parts := strings.Split(rest, "_")
	if len(parts) != 2 {
		return nil, domain.ErrInvalidToken
	}
	enrollPrefix, enrollSecret := parts[0], parts[1]

	agentPlaintext, agentPrefix, agentHash, err := MintAgentToken()
	if err != nil {
		return nil, err
	}

	verify := func(stored *domain.Runner) error {
		// Map "secret mismatch" to ErrInvalidToken so the repo surfaces
		// it verbatim without inspecting bcrypt internals.
		if err := bcrypt.CompareHashAndPassword(
			[]byte(stored.EnrollTokenHash),
			[]byte(enrollSecret),
		); err != nil {
			return domain.ErrInvalidToken
		}
		return nil
	}

	if len(capabilities) == 0 {
		capabilities = []byte("{}")
	}
	runner, err := e.repo.RedeemEnrollment(ctx, enrollPrefix, verify, domain.NewAgentToken{
		Prefix: agentPrefix,
		Hash:   string(agentHash),
	}, capabilities)
	if err != nil {
		return nil, err
	}
	return &domain.RedeemEnrollResult{
		Runner:              runner,
		AgentTokenPlaintext: agentPlaintext,
		Capabilities:        capabilities,
	}, nil
}
