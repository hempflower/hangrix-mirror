package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agents_config/domain"
)

// lockWire mirrors `.hangrix/agents.lock` on the wire. The `agents`
// list — not a map — is the disk shape so duplicates can be flagged
// before the lifting step collapses to a domain map.
type lockWire struct {
	Version int             `yaml:"version"`
	Agents  []lockEntryWire `yaml:"agents"`
}

type lockEntryWire struct {
	Ref         string    `yaml:"ref"`
	ResolvedSHA string    `yaml:"resolved_sha"`
	ResolvedAt  time.Time `yaml:"resolved_at"`
}

// ParseLockFile decodes a `.hangrix/agents.lock` body.
//
// Validation rules:
//   - version == 1
//   - every entry has non-empty ref + parseable AgentRef
//   - every entry has non-empty resolved_sha (40 lowercase hex chars)
//   - every entry has non-zero resolved_at
//   - no duplicate ref keys
//
// TODO(M7a Phase 2): wire a resolver that turns an unresolved AgentRef
// into a fresh LockEntry by hitting the agent-repo's git tree. The
// parser purposely stays I/O-free; resolution lives one layer up.
func ParseLockFile(body []byte) (*domain.LockFile, error) {
	var wire lockWire
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&wire); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf(".hangrix/agents.lock is empty")
		}
		return nil, fmt.Errorf("%w: %s", domain.ErrUnknownField, err.Error())
	}

	if wire.Version != 1 {
		return nil, fmt.Errorf("%w: got %d, want 1", domain.ErrInvalidVersion, wire.Version)
	}

	out := make(map[string]domain.LockEntry, len(wire.Agents))
	for i, e := range wire.Agents {
		ref, err := domain.ParseAgentRef(e.Ref)
		if err != nil {
			return nil, fmt.Errorf("agents[%d].ref: %w", i, err)
		}
		key := LockKey(ref)
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("%w: %q", domain.ErrDuplicateLockKey, key)
		}
		if !isValidSHA40(e.ResolvedSHA) {
			return nil, fmt.Errorf("%w: agents[%d].resolved_sha=%q", domain.ErrInvalidLockEntry, i, e.ResolvedSHA)
		}
		if e.ResolvedAt.IsZero() {
			return nil, fmt.Errorf("%w: agents[%d].resolved_at is zero", domain.ErrInvalidLockEntry, i)
		}
		out[key] = domain.LockEntry{
			ResolvedSHA: e.ResolvedSHA,
			ResolvedAt:  e.ResolvedAt.UTC(),
		}
	}

	return &domain.LockFile{
		Version: wire.Version,
		Agents:  out,
	}, nil
}

// SerializeLockFile renders a LockFile back to deterministic YAML bytes.
// Keys are sorted lexicographically so the same logical lock content
// produces byte-identical output run-to-run — git diff stays meaningful
// and CI lock-drift checks work.
func SerializeLockFile(lf *domain.LockFile) ([]byte, error) {
	if lf.Version != 1 {
		return nil, fmt.Errorf("%w: got %d, want 1", domain.ErrInvalidVersion, lf.Version)
	}

	keys := make([]string, 0, len(lf.Agents))
	for k := range lf.Agents {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	entries := make([]lockEntryWire, 0, len(keys))
	for _, k := range keys {
		v := lf.Agents[k]
		if !isValidSHA40(v.ResolvedSHA) {
			return nil, fmt.Errorf("%w: %q resolved_sha=%q", domain.ErrInvalidLockEntry, k, v.ResolvedSHA)
		}
		if v.ResolvedAt.IsZero() {
			return nil, fmt.Errorf("%w: %q resolved_at is zero", domain.ErrInvalidLockEntry, k)
		}
		entries = append(entries, lockEntryWire{
			Ref:         k,
			ResolvedSHA: v.ResolvedSHA,
			ResolvedAt:  v.ResolvedAt.UTC(),
		})
	}

	wire := lockWire{Version: lf.Version, Agents: entries}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&wire); err != nil {
		return nil, fmt.Errorf("encode lock: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close encoder: %w", err)
	}
	return buf.Bytes(), nil
}

// LockKey is the canonical map key for LockFile.Agents — exactly the
// AgentRef wire form. Centralised so callers can't accidentally
// hand-format a key with the wrong separator.
func LockKey(ref domain.AgentRef) string {
	return ref.String()
}

// isValidSHA40 reports whether s is 40 lowercase hex digits.
//
// We require the full sha rather than abbreviations: the lock file is
// the cross-reference everyone hands around for audit, and short-sha
// ambiguity has bitten Git tooling before.
func isValidSHA40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}
