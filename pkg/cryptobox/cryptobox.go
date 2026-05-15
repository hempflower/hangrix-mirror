// Package cryptobox is a thin AES-256-GCM helper used to encrypt small
// secrets at rest in the database (currently: per-provider LLM api keys).
//
// Wire format of an encrypted value is base64(nonce || ciphertext_with_tag)
// where nonce is the 12-byte GCM nonce. Storing nonce + ciphertext together
// keeps the schema simple — one TEXT column per secret — and lets us rotate
// keys without a separate nonce migration.
//
// The master key is provided once at startup via config.LLM.EncryptionKey.
// The key MUST be exactly 32 bytes after base64-decoding; we fail loudly at
// boot rather than letting a weak/short key silently degrade security.
package cryptobox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// KeyLen is the required AES-256 key length in bytes.
const KeyLen = 32

// ErrInvalidCiphertext is returned by Decrypt when the input is malformed —
// wrong length, bad base64, or the GCM tag fails to authenticate.
var ErrInvalidCiphertext = errors.New("cryptobox: invalid ciphertext")

// Box is an AES-256-GCM encryptor/decryptor bound to a single key. Safe for
// concurrent use across goroutines (cipher.AEAD is documented as such).
type Box struct {
	aead cipher.AEAD
}

// New constructs a Box from a base64-encoded 32-byte key. Returns an error
// if the key is malformed; callers (typically the ioc layer) should panic
// at startup so misconfiguration is loud.
func New(base64Key string) (*Box, error) {
	if base64Key == "" {
		return nil, errors.New("cryptobox: empty key (set llm.encryption_key in config)")
	}
	key, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("cryptobox: decode key: %w", err)
	}
	if len(key) != KeyLen {
		return nil, fmt.Errorf("cryptobox: key must be %d bytes after base64-decode, got %d", KeyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cryptobox: aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cryptobox: gcm: %w", err)
	}
	return &Box{aead: aead}, nil
}

// GenerateKey returns a fresh base64-encoded 32-byte key suitable for
// `llm.encryption_key`. Used by ad-hoc CLI tooling / setup docs; the server
// never calls this on its own.
func GenerateKey() (string, error) {
	buf := make([]byte, KeyLen)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// Encrypt seals plaintext with a fresh random nonce and returns
// base64(nonce || ciphertext_with_tag).
func (b *Box) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nil, nonce, []byte(plaintext), nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt reverses Encrypt. Returns ErrInvalidCiphertext for any failure
// (bad base64, short input, tag mismatch) so callers don't need to
// distinguish — these are all "untrusted input" cases.
func (b *Box) Decrypt(ciphertext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	nonceSize := b.aead.NonceSize()
	if len(raw) < nonceSize {
		return "", ErrInvalidCiphertext
	}
	nonce, ct := raw[:nonceSize], raw[nonceSize:]
	pt, err := b.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	return string(pt), nil
}
