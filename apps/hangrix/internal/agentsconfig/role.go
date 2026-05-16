package agentsconfig

// Role is one entry in the host yaml's `roles:` map after parsing. The
// map key (the role's identifier like "backend" / "reviewer") lives on
// HostConfig.Roles rather than here so a Role can be passed around
// without dragging the key along.
type Role struct {
	// Agent is the parsed reference to the agent repo + revision that
	// implements this role. The lock file resolves Ref to a sha at
	// session-spawn time.
	Agent AgentRef

	// Triggers is the event subscription. Non-empty and every entry
	// is a recognised Trigger constant — the parser rejects unknown
	// names rather than ignoring them, so a typo can't silently
	// disable the role.
	Triggers []string

	// Can is the platform tool ACL for this role. The agent repo's
	// declared_tools is documentation; this list is the actual grant.
	// Service layers higher up (the runner / dispatcher) consult this
	// before allowing a tool call. Empty list means "no platform
	// tools" — useful for roles that only run the LLM (e.g. summary
	// bots).
	Can []string

	// Scope is a soft constraint on which files the role typically
	// touches. It is injected into the role's prompt for dispatcher
	// hinting and not enforced by pre-receive hooks.
	Scope Scope

	// MentionBy gates which actor class can wake the role via
	// `@agent-<role-key>`. The empty value here means "not set in
	// yaml"; service/normalize.go fills the default
	// (MentionByCollaborators) after validation runs so the parser
	// can keep absence-vs-explicit distinguishable.
	MentionBy MentionBy

	// Prompt is the host-side addendum appended after the agent's
	// base prompt. Mutually exclusive with PromptFile.
	Prompt string

	// PromptFile is a repo-relative path to the host addendum
	// content. Must start with `.hangrix/prompts/` so the host
	// directory layout stays predictable. Mutually exclusive with
	// Prompt.
	PromptFile string

	// LLM is the per-role LLM override. nil means "inherit team
	// default"; a non-nil zero-value struct is rejected by the
	// parser (empty model is invalid).
	LLM *LLMConfig
}

// Scope is the soft path-glob constraint declared by a role.
type Scope struct {
	// Paths is a list of glob patterns; the dispatcher treats them
	// as hints, not enforcement. Empty slice means "no scope hint".
	Paths []string
}
