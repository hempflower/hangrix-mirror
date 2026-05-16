// Package agentsconfig parses the M7a multi-role yaml configs that drive
// agent / host collaboration:
//
//   - `agent.yml`              — root manifest of an agent repository.
//   - `.hangrix/agents.yml`    — host repo team config (roles, container,
//                                 secrets, llm).
//   - `.hangrix/agents.lock`   — pinned `agent.yml` ref → sha resolution.
//
// Pure-function parsing + validation; no DB, no HTTP, no I/O. Lives
// alongside `app/`, `config/`, `database/`, `web/` rather than under
// `internal/modules/` because it is a utility library, not a business
// module — multiple modules consume it (repo, runner, the future M7a
// session-spawn orchestrator).
//
// Wire-format decoders use yaml.v3 with KnownFields(true) so unknown
// keys are an error, not a warning — schema strictness is the whole
// point per docs/agent-config.md principle 7. Decoders work on private
// wire structs and lift cleanly into the exported value types in this
// same package; yaml struct tags are deliberately confined to the wire
// layer.
//
// File I/O (reading the bytes off disk / out of a git tree) belongs to
// the caller; every entry point takes []byte. Lock-file ref → sha
// resolution belongs to M7a Phase 2 and is marked with a TODO seam
// near `ParseLockFile`.
//
// No ioc Module — all entry points are package-level pure functions,
// so consumers import the package and call directly. No state to bind.
package agentsconfig

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
