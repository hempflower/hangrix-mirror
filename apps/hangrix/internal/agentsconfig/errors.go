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

	// ErrUnknownTrigger fires when a role declares a trigger event the
	// platform does not emit. The allow-list lives in triggers.go.
	ErrUnknownTrigger = errors.New("unknown trigger event")

	// ErrEmptyTriggers fires when a role has zero triggers — the spec
	// requires every role to subscribe to at least one event.
	ErrEmptyTriggers = errors.New("role must declare at least one trigger")

	// ErrInvalidTriggerSpec fires when the per-trigger filter block is
	// the wrong shape (not a mapping, unknown filter key, or filters
	// applied to an event that does not accept them).
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

	// ErrInvalidSecretName fires for the same shape constraint applied
	// to entries in the `secrets:` list.
	ErrInvalidSecretName = errors.New("secret name must match [A-Z_][A-Z0-9_]*")

	// ErrInvalidVolumeMount fires when a volume mount path is not
	// absolute, contains `..`, or has an empty name.
	ErrInvalidVolumeMount = errors.New("invalid volume mount")

	// ErrInvalidModel fires when llm.model is empty.
	ErrInvalidModel = errors.New("llm.model is required when llm is present")

	// ErrInvalidLLMParam fires for out-of-range temperature / top_p,
	// negative max_output_tokens / max_context_tokens, or a
	// reasoning_effort outside the allowed enum.
	ErrInvalidLLMParam = errors.New("invalid llm parameter")

	// ErrDuplicateRoleKey fires when a role key appears twice in the
	// host yaml's `roles:` map. yaml.v3 normally returns the last value
	// silently — the parser explicitly catches duplicates.
	ErrDuplicateRoleKey = errors.New("duplicate role key")
)
