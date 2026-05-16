package agentsconfig

import "time"

// LockFile is the parsed `.hangrix/agents.lock`. It pins every
// `<owner>/<name>@<ref>` the host yaml references to a concrete sha at
// resolve time — the package-lock model. The runner pulls the sha, not
// the ref, so a moving branch tip can't change what spawns in a future
// session.
type LockFile struct {
	// Version pins the lock schema (1 in M7a).
	Version int

	// Agents maps `<owner>/<name>@<ref>` (the canonical AgentRef
	// String() form) → resolved entry. The key is intentionally the
	// full ref including `@<ref>` so the same agent at different tags
	// can coexist in one lock file.
	Agents map[string]LockEntry
}

// LockEntry is one resolved row in the lock file.
type LockEntry struct {
	// ResolvedSHA is the agent-repo commit the ref was pointing at
	// when the lock was last refreshed. Always a full 40-char sha;
	// the parser validates the shape but does not contact the repo
	// store.
	ResolvedSHA string

	// ResolvedAt records when the resolver wrote this row. Used by
	// the UI to surface "lock is stale, refresh available" hints; the
	// runtime never reads it for behaviour.
	ResolvedAt time.Time
}
