package agentsconfig

// Role is one entry in the host yaml's `roles:` map after parsing. The
// map key (the role's identifier like "backend" / "reviewer") lives on
// HostConfig.Roles rather than here so a Role can be passed around
// without dragging the key along.
type Role struct {
	// Triggers is the role's event subscription map. Keys are
	// recognised Trigger constants; values carry the per-event filter
	// block (paths for commit.pushed, mentioned_only / from_roles /
	// from_users for issue.comment). Triggers without a relevant
	// filter use the zero TriggerSpec (`event-name: {}` in yaml).
	//
	// The map is guaranteed non-empty by the parser; a role with no
	// triggers is a misconfiguration. Map order is irrelevant —
	// dispatchers iterate by event name during fan-out, not by
	// declaration order.
	Triggers map[Trigger]*TriggerSpec

	// Can is the platform tool ACL whitelist for this role. Service
	// layers higher up (the runner / dispatcher) consult this before
	// allowing a tool call. Empty list AND empty Not means "no
	// platform tools" — useful for roles that only run the LLM (e.g.
	// summary bots). When Can is non-empty it takes precedence over
	// Not (whitelist wins on conflict).
	Can []string

	// Not is the platform tool ACL blacklist for this role. It
	// applies only when Can is empty: in that mode the role gets
	// "every registered tool except the ones listed here". Empty
	// list (the common case) leaves the whitelist in charge.
	Not []string

	// Scope is a soft constraint on which files the role typically
	// touches. It is injected into the role's prompt for dispatcher
	// hinting and not enforced by pre-receive hooks.
	Scope Scope

	// Prompt is the role's full system prompt (inline form).
	// Mutually exclusive with PromptFile; exactly one MUST be set.
	Prompt string

	// PromptFile is a repo-relative path to the role's prompt body.
	// Must start with `.hangrix/prompts/` so the host directory
	// layout stays predictable. Mutually exclusive with Prompt.
	PromptFile string

	// MCP is the MCP server whitelist for this role. Each element
	// names a server key from the repo-root .mcp.json. When empty
	// (nil or zero-length), the role does not load any MCP servers.
	// When non-empty, only the listed servers are loaded; any
	// server referenced but missing from .mcp.json causes the
	// agent session to explicitly fail at startup rather than
	// silently degrade.
	MCP []string

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
