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

	// ErrUnknownField is the canonical wrapper for any yaml.v3
	// KnownFields(true) rejection — yaml's own message already names
	// the field, so the sentinel only needs to discriminate this class.
	ErrUnknownField = errors.New("unknown field in config")

	// ErrAgentSchemaForbiddenField fires when an agent.yml contains a
	// key that belongs in the host config (container, env, secrets,
	// volumes, llm, roles). The host/agent split is principle 7 — the
	// agent repo must not pin a toolchain.
	ErrAgentSchemaForbiddenField = errors.New("field forbidden in agent.yml schema")

	// ErrInvalidKind fires when an agent.yml's `kind:` is not "agent".
	ErrInvalidKind = errors.New("invalid kind for agent manifest")

	// ErrMissingBasePrompt fires when `entry.base_prompt` is empty.
	ErrMissingBasePrompt = errors.New("entry.base_prompt is required")

	// ErrInvalidBasePromptPath fires for absolute paths, ".." escapes,
	// or other shapes that would let an agent manifest reference files
	// outside its own repo root.
	ErrInvalidBasePromptPath = errors.New("entry.base_prompt must be a relative path under the repo root")

	// ErrInvalidDeclaredTool fires for empty or non-slug tool names in
	// the agent manifest's `declared_tools` list.
	ErrInvalidDeclaredTool = errors.New("declared_tools entry must be a non-empty slug")

	// ErrMissingAgentRef fires when a host yaml role omits the
	// `@<ref>` suffix on its `agent:` value. We reject empty ref
	// deliberately: floating refs (no pin) would defeat the lock-file
	// model that ties session audit to a concrete commit.
	ErrMissingAgentRef = errors.New("agent reference must include @<ref>")

	// ErrInvalidAgentRef fires for any other malformed agent ref —
	// missing owner, missing name, illegal characters.
	ErrInvalidAgentRef = errors.New("invalid agent reference")

	// ErrInvalidMentionBy fires when mention_by is set to something
	// other than owner / collaborators / anyone.
	ErrInvalidMentionBy = errors.New("invalid mention_by value")

	// ErrUnknownTrigger fires when a role declares a trigger event the
	// platform does not emit. The allow-list lives in triggers.go.
	ErrUnknownTrigger = errors.New("unknown trigger event")

	// ErrEmptyTriggers fires when a role has zero triggers — the spec
	// requires every role to subscribe to at least one event.
	ErrEmptyTriggers = errors.New("role must declare at least one trigger")

	// ErrPromptMutuallyExclusive fires when a role sets both `prompt`
	// and `prompt_file`. They must be at most one.
	ErrPromptMutuallyExclusive = errors.New("prompt and prompt_file are mutually exclusive")

	// ErrInvalidPromptFilePath fires when `prompt_file` does not begin
	// with `.hangrix/prompts/` or escapes via `..`.
	ErrInvalidPromptFilePath = errors.New("prompt_file must start with .hangrix/prompts/")

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

	// ErrInvalidSecretName fires for the same shape constraint applied
	// to entries in the `secrets:` list.
	ErrInvalidSecretName = errors.New("secret name must match [A-Z_][A-Z0-9_]*")

	// ErrInvalidVolumeMount fires when a volume mount path is not
	// absolute, contains `..`, or has an empty name.
	ErrInvalidVolumeMount = errors.New("invalid volume mount")

	// ErrInvalidModel fires when llm.model is empty.
	ErrInvalidModel = errors.New("llm.model is required when llm is present")

	// ErrInvalidLLMParam fires for out-of-range temperature / top_p
	// or non-positive max_tokens.
	ErrInvalidLLMParam = errors.New("invalid llm parameter")

	// ErrDuplicateRoleKey fires when a role key appears twice in the
	// host yaml's `roles:` map. yaml.v3 normally returns the last value
	// silently — the parser explicitly catches duplicates.
	ErrDuplicateRoleKey = errors.New("duplicate role key")

	// ErrDuplicateLockKey fires when the lock file lists the same
	// owner/name@ref pair twice.
	ErrDuplicateLockKey = errors.New("duplicate lock key")

	// ErrInvalidLockEntry fires for empty resolved_sha or zero
	// resolved_at on a lock entry.
	ErrInvalidLockEntry = errors.New("invalid lock entry")
)
