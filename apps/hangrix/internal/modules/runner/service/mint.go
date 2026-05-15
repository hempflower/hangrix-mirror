// Token-minting helpers shared across the runner service. Each
// `Mint<X>Token` function generates a wire-format plaintext + its
// public prefix + bcrypt(secret) so callers can hand the credential
// to the user (plaintext, shown once) and Repo (prefix + hash, stored
// forever) without re-deriving anything.
//
// Crypto + wire format both live here because:
//   - bcrypt is a service-layer concern (persistence stores opaque
//     hashes; the crypto choice is independent of the schema).
//   - The wire prefix (`hgxe_` / `hgxr_` / `hgxs_`) is a public
//     contract the same package's validators enforce on the read
//     path — keeping mint + verify in one place makes drift obvious.
package service

import (
	"crypto/rand"

	"golang.org/x/crypto/bcrypt"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// alphabet is shared with PATs / session tokens: [A-Za-z0-9],
// URL-safe and easy to paste in a terminal.
const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// MintAgentToken generates a fresh `hgxr_<prefix>_<secret>` wire
// token (the long-lived runner credential) and returns
// (plaintext, prefix, bcrypt(secret)). Called by the enrollment
// service when issuing a runner's agent token.
func MintAgentToken() (plaintext, prefix string, hashed []byte, err error) {
	p, secret, h, err := mintToken()
	if err != nil {
		return "", "", nil, err
	}
	return domain.AgentTokenWirePrefix + p + "_" + secret, p, h, nil
}

// MintSessionToken generates a fresh `hgxs_<prefix>_<secret>` wire
// token (the per-session agent identity). Called by the admin
// handler when seeding a new agent_session row.
func MintSessionToken() (plaintext, prefix string, hashed []byte, err error) {
	p, secret, h, err := mintToken()
	if err != nil {
		return "", "", nil, err
	}
	return domain.SessionTokenWirePrefix + p + "_" + secret, p, h, nil
}

// MintEnrollToken generates a fresh `hgxe_<prefix>_<secret>` wire
// token (the one-shot enrollment artefact). Called by the runner
// admin when creating a new runner row — the platform shows the
// plaintext once and the runner CLI redeems it via /api/runner/enroll.
func MintEnrollToken() (plaintext, prefix string, hashed []byte, err error) {
	p, secret, h, err := mintToken()
	if err != nil {
		return "", "", nil, err
	}
	return domain.EnrollTokenWirePrefix + p + "_" + secret, p, h, nil
}

// mintToken is the shared prefix+secret+hash core. The split between
// prefix length and secret length is a domain constant; bcrypt cost
// is the library default.
func mintToken() (prefix, secret string, hashed []byte, err error) {
	prefix, err = randString(domain.TokenPrefixLen)
	if err != nil {
		return "", "", nil, err
	}
	secret, err = randString(domain.TokenSecretLen)
	if err != nil {
		return "", "", nil, err
	}
	hashed, err = bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", "", nil, err
	}
	return
}

// randString draws n bytes from crypto/rand and maps each into the
// alphabet. The 62-char alphabet doesn't evenly divide 256 so the
// distribution is *very* slightly biased toward the first 256%62=8
// chars; for 8- and 32-char tokens that's well below the
// bcrypt+collision-retry risk threshold, so we keep the simple modulo
// mapping.
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
