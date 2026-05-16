package domain

import (
	"fmt"
	"strings"
)

// AgentRef identifies a specific commit-shaped revision of an agent
// repository. The wire form is `<owner>/<name>@<ref>` where `<ref>` is a
// tag, branch, or sha. The `@<ref>` segment is REQUIRED — the platform
// refuses to consume a floating reference because the lock-file model
// (host yaml says `@v1.2.3`, lock pins it to a sha) loses its anchor if
// the source value is empty.
type AgentRef struct {
	// Owner is the agent repo owner (user or org). Lowercased
	// alphanumerics + hyphens; validated against the same rules as
	// a host owner.
	Owner string

	// Name is the agent repo name.
	Name string

	// Ref is the requested revision — typically a semver tag, but
	// branch names and full shas are also accepted at this layer. The
	// lock file resolves it to a sha at runtime.
	Ref string
}

// String renders the canonical wire form. Round-trips through
// ParseAgentRef without information loss.
func (r AgentRef) String() string {
	return r.Owner + "/" + r.Name + "@" + r.Ref
}

// ParseAgentRef decodes the canonical `<owner>/<name>@<ref>` form.
// Empty ref, missing owner, or missing name all return ErrMissingAgentRef
// / ErrInvalidAgentRef so callers can distinguish "floating reference"
// from other shape errors.
func ParseAgentRef(s string) (AgentRef, error) {
	if s == "" {
		return AgentRef{}, fmt.Errorf("%w: empty", ErrInvalidAgentRef)
	}

	at := strings.IndexByte(s, '@')
	if at < 0 {
		return AgentRef{}, fmt.Errorf("%w: missing @<ref> in %q", ErrMissingAgentRef, s)
	}
	if at == len(s)-1 {
		return AgentRef{}, fmt.Errorf("%w: empty ref after @ in %q", ErrMissingAgentRef, s)
	}

	left, ref := s[:at], s[at+1:]
	// Trailing or leading whitespace anywhere is a misconfiguration —
	// the YAML parser strips outer whitespace but inner whitespace
	// here is always a typo.
	if strings.ContainsAny(s, " \t\n") {
		return AgentRef{}, fmt.Errorf("%w: whitespace in %q", ErrInvalidAgentRef, s)
	}
	// A second `@` in the ref portion is ambiguous — could mean
	// `user@host@ref` (no, that is not a thing in git refs we
	// support).
	if strings.ContainsRune(ref, '@') {
		return AgentRef{}, fmt.Errorf("%w: multiple @ in %q", ErrInvalidAgentRef, s)
	}

	slash := strings.IndexByte(left, '/')
	if slash < 0 {
		return AgentRef{}, fmt.Errorf("%w: missing /<name> in %q", ErrInvalidAgentRef, s)
	}
	owner, name := left[:slash], left[slash+1:]
	if owner == "" {
		return AgentRef{}, fmt.Errorf("%w: empty owner in %q", ErrInvalidAgentRef, s)
	}
	if name == "" {
		return AgentRef{}, fmt.Errorf("%w: empty name in %q", ErrInvalidAgentRef, s)
	}
	if strings.ContainsRune(name, '/') {
		// owner/name/extra@ref — extra path segments are invalid.
		return AgentRef{}, fmt.Errorf("%w: too many / in %q", ErrInvalidAgentRef, s)
	}

	return AgentRef{Owner: owner, Name: name, Ref: ref}, nil
}
