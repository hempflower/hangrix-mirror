package domain

// AgentManifest models the parsed `agent.yml` that lives at the root of
// an agent repository. It is intentionally narrow: principle 7 forbids
// the agent repo from pinning toolchain / image / env / secrets — those
// are host-side concerns. Anything outside this struct in the YAML is
// rejected by the parser, not silently dropped.
type AgentManifest struct {
	// Version pins the schema. Only `1` is accepted in M7a; future
	// bumps add explicit cases rather than range-checking.
	Version int

	// Kind is always "agent" in M7a. The field exists so future
	// manifest kinds (e.g. "tool", "preset") can share the same root
	// filename without ambiguity.
	Kind string

	// Entry describes how to materialize the agent's base prompt.
	Entry Entry

	// DeclaredTools is the agent author's documentation of what
	// platform tools the agent expects. It is NOT an authorization —
	// host yaml's `can:` is the only enforced ACL. Service-level
	// dispatchers may surface a warning when a host grants tools the
	// agent did not declare, but the platform never refuses to run on
	// that mismatch alone.
	DeclaredTools []string
}

// Entry is the parsed `entry:` block on an agent manifest.
type Entry struct {
	// BasePrompt is a repo-relative path to the file the runner
	// loads as the agent's base system prompt (typically
	// `prompts/system.md`). The parser rejects leading `/` and `..`
	// segments — base prompts must live inside the agent repo.
	BasePrompt string
}
