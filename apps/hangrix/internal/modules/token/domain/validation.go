// Validation helpers for token inputs. Pure domain — no I/O, no crypto.
// Service callers invoke these before handing inputs to Repo so
// persistence stays free of policy.
package domain

import (
	"fmt"
	"strings"
)

// ValidateName trims whitespace and enforces the length window. Returns
// the trimmed value alongside the error so callers can use one
// derived-state path instead of re-trimming.
func ValidateName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || len(n) > MaxNameLen {
		return "", ErrInvalidName
	}
	return n, nil
}

// ValidateScopes asserts every scope in the slice is a recognised enum.
// Empty slice is allowed — the handler treats no-scope tokens as
// read-only by default elsewhere.
func ValidateScopes(scopes []Scope) error {
	for _, sc := range scopes {
		if !sc.Valid() {
			return fmt.Errorf("%w: %q", ErrInvalidScope, sc)
		}
	}
	return nil
}
