// Ref-name policy. Pure-domain validation — no I/O, no go-git
// dependency. Infra calls this before any persistence to avoid
// surfacing wrapped go-git errors for the obviously-bad shapes the
// caller can fix client-side.
package domain

import "strings"

// maxRefSegmentLen caps the per-segment length. 200 is a generous
// upper bound on what humans reasonably name a branch / tag;
// anything longer is almost certainly user error or a malicious
// payload trying to overflow a downstream UI column.
const maxRefSegmentLen = 200

// IsValidRefName reports whether a single ref segment (branch or tag
// name, without the `refs/heads/` or `refs/tags/` prefix) is well-
// formed. Returns true for valid segments and false for anything the
// platform should reject without a DB / disk round-trip. Mirrors a
// subset of git's check-ref-format rules — full grammar enforcement
// still happens via go-git's ReferenceName.Validate() when the
// segment is composed into a fully-qualified ref.
//
// Rules:
//
//   - non-empty, ≤ 200 chars
//   - must not start with '-' or '/'
//   - must not end with '/'
//   - must not contain '..' or '//'
//   - must not contain control bytes (≤ 0x20, == 0x7f)
//   - must not contain '~ ^ : ? * [ \\' (rejected by git itself)
func IsValidRefName(name string) bool {
	if name == "" || len(name) > maxRefSegmentLen {
		return false
	}
	if strings.HasPrefix(name, "-") ||
		strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "//") {
		return false
	}
	for _, r := range name {
		if r <= 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case '~', '^', ':', '?', '*', '[', '\\':
			return false
		}
	}
	return true
}
