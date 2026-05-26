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

	// Permission is the role's GitHub-style repo permission level:
	// "read" or "write". It is the coarse, server-enforced access
	// boundary on the platform v1 REST API — "read" roles may call
	// read-only endpoints (GET issue/comment/todo/contribution/…),
	// "write" roles may additionally mutate (comment, edit, merge,
	// release, …). Empty defaults to "read" (fail-safe: a role that
	// forgets the field cannot mutate). Fine-grained per-tool control
	// is NOT done here — see Tools.
	Permission string

	// Tools is the list of tool-rule names this role references (the
	// `tools:` map in agents.yml). Each named rule is a reusable
	// whitelist of platform tool-name globs; the role's platform-tool
	// visibility is the union of every referenced rule's patterns,
	// resolved into ToolPatterns at load time. Local tools (read/write/
	// edit/glob/grep/bash/webfetch) are never restricted by this and are
	// always available. Empty means the role sees no platform tools.
	Tools []string

	// ToolPatterns is the resolved union of glob patterns from the rules
	// named in Tools, computed when the host config is assembled. It is
	// the agent-side platform-tool whitelist (schema-hiding), injected
	// into the agent as HANGRIX_PLATFORM_TOOLS and expanded there against
	// the platform tool registry. The server does NOT enforce it — only
	// Permission gates the v1 API.
	ToolPatterns []string

	// Scope is a soft constraint on which files the role typically
	// touches. It is injected into the role's prompt for dispatcher
	// hinting and not enforced by pre-receive hooks.
	Scope Scope

	// Prompt is the role's full system prompt — the Markdown body of the
	// role's `.hangrix/agents/<role>.md` file (everything after the YAML
	// front matter). Always set; a role with an empty body is rejected.
	Prompt string

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
