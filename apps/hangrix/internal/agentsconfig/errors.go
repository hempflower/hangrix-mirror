package agentsconfig

import "errors"

// Sentinel errors. Service-layer parsers wrap these with %w-style context
// (`fmt.Errorf("roles.backend.triggers: %w", ErrUnknownTrigger)`) so
// callers can both branch on the sentinel and surface the offending
// field. The set intentionally over-enumerates rather than collapsing
// into a single ErrInvalidConfig — schema-strictness (principle 7) is
// the whole point of this module.
var (
	// ErrInvalidVersion fires when a config's `version:` is missing or
	// not 1. Future schema bumps will widen this to a known-set check.
	ErrInvalidVersion = errors.New("invalid config version")

	// ErrUnknownTrigger fires when a role declares a trigger event the
	// platform does not emit. The allow-list lives in triggers.go.
	ErrUnknownTrigger = errors.New("unknown trigger event")

	// ErrEmptyTriggers fires when a role has zero triggers — the spec
	// requires every role to subscribe to at least one event.
	ErrEmptyTriggers = errors.New("role must declare at least one trigger")

	// ErrInvalidTriggerSpec fires when the per-trigger filter block is
	// the wrong shape (not a mapping, malformed value type, or filters
	// applied to an event that does not accept them). Unknown filter
	// keys are silently dropped — see commentFilterWire for the
	// forward-compat rationale.
	ErrInvalidTriggerSpec = errors.New("invalid trigger filter spec")

	// ErrPromptMutuallyExclusive fires when a role sets both `prompt`
	// and `prompt_file`. They must be at most one.
	ErrPromptMutuallyExclusive = errors.New("prompt and prompt_file are mutually exclusive")

	// ErrInvalidPromptFilePath fires when `prompt_file` does not begin
	// with `.hangrix/prompts/` or escapes via `..`.
	ErrInvalidPromptFilePath = errors.New("prompt_file must start with .hangrix/prompts/")

	// ErrPromptMissing fires when a role has neither `prompt:` nor
	// `prompt_file:`. Host yaml is the only place a prompt can live —
	// a role with no prompt source has nothing to run.
	ErrPromptMissing = errors.New("role must declare one of prompt or prompt_file")

	// ErrInvalidRoleKey fires when a role key violates the
	// `^[a-z][a-z0-9-]{0,38}$` grammar — the same shape used in the
	// mention protocol (`@agent-<role-key>`).
	ErrInvalidRoleKey = errors.New("invalid role key")

	// ErrEmptyRoles fires when the host yaml's `roles:` map is empty.
	// A host config with no roles is a misconfiguration, not a valid
	// degenerate case.
	ErrEmptyRoles = errors.New("host config must declare at least one role")

	// ErrContainerSourceConflict fires when both `image:` and `build:`
	// are set on the container, or when neither is set.
	ErrContainerSourceConflict = errors.New("container must declare exactly one of image or build")

	// ErrInvalidEnvKey fires when an env var key is empty or contains
	// chars outside [A-Z0-9_] or starts with a digit.
	ErrInvalidEnvKey = errors.New("env key must match [A-Z_][A-Z0-9_]*")

	// ErrInvalidVolumeMount fires when a volume mount path is not
	// absolute, contains `..`, or has an empty name.
	ErrInvalidVolumeMount = errors.New("invalid volume mount")

	// ErrInvalidContainerEntrypoint fires when container.entrypoint
	// contains an empty string. A nil / omitted list is fine (it
	// means "use the runner's built-in sleep default").
	ErrInvalidContainerEntrypoint = errors.New("invalid container entrypoint")

	// ErrInvalidModel fires when llm.model is empty.
	ErrInvalidModel = errors.New("llm.model is required when llm is present")

	// ErrInvalidLLMParam fires for out-of-range temperature / top_p
	// or negative max_output_tokens / max_context_tokens.
	ErrInvalidLLMParam = errors.New("invalid llm parameter")

	// ErrDuplicateRoleKey fires when a role key appears twice in the
	// host yaml's `roles:` map. yaml.v3 normally returns the last value
	// silently — the parser explicitly catches duplicates.
	ErrDuplicateRoleKey = errors.New("duplicate role key")

	// ErrInvalidMCP fires when a role's `mcp:` list contains an empty
	// server name. The agent-runtime owns the server-existence check.
	ErrInvalidMCP = errors.New("invalid mcp server name")

	// ErrInvalidReviewers fires when the `reviewers:` block is malformed: a
	// rule missing paths / reviewers, an empty fallback, or a reviewer role
	// that does not exist or cannot cast review votes.
	ErrInvalidReviewers = errors.New("invalid reviewers config")

	// ErrInvalidPermission fires when a role's `permission:` is set to
	// anything other than "read" or "write". Empty is allowed and
	// defaults to "read".
	ErrInvalidPermission = errors.New("role permission must be read or write")

	// ErrInvalidToolRule fires when the `tools:` rule map is malformed
	// (empty rule name or empty glob pattern), or when a role references
	// a rule name that is not declared in the host's `tools:` map.
	ErrInvalidToolRule = errors.New("invalid tool rule")

	// ErrInvalidAgentFile fires when a `.hangrix/agents/<role>.md` file
	// is malformed: missing/unterminated YAML front matter, an empty
	// prompt body, or a filename that is not a valid role key.
	ErrInvalidAgentFile = errors.New("invalid agent file")
)
