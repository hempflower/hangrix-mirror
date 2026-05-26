package service

import (
	"time"

	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// lastActivityAt returns the most recent activity timestamp for the session
// by taking the maximum of container_last_used_at, ended_at, started_at,
// claimed_at, and created_at.
func lastActivityAt(s *runnerdomain.AgentSession) time.Time {
	latest := s.CreatedAt
	if s.ClaimedAt != nil && s.ClaimedAt.After(latest) {
		latest = *s.ClaimedAt
	}
	if s.StartedAt != nil && s.StartedAt.After(latest) {
		latest = *s.StartedAt
	}
	if s.EndedAt != nil && s.EndedAt.After(latest) {
		latest = *s.EndedAt
	}
	if s.ContainerLastUsedAt != nil && s.ContainerLastUsedAt.After(latest) {
		latest = *s.ContainerLastUsedAt
	}
	return latest
}

// truncateSuffix is appended by truncateBody when the input exceeds
// maxRunes. Its length is reserved from the maxRunes budget so the
// returned string never overflows the cap.
const truncateSuffix = "… (truncated)"

// truncateBody returns s unchanged when it fits within maxRunes Unicode
// characters (runes). Longer strings are shortened so that the returned
// text — including the "… (truncated)" suffix — does not exceed maxRunes.
func truncateBody(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	suffixRunes := []rune(truncateSuffix)
	budget := maxRunes - len(suffixRunes)
	if budget < 0 {
		return string(runes[:maxRunes])
	}
	return string(runes[:budget]) + truncateSuffix
}
