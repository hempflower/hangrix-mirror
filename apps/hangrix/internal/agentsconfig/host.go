package agentsconfig

// HostConfig models the parsed `.hangrix/agents.yml`. It is the single
// source of truth for which roles a host repo runs, in what container,
// with which secrets, under whose LLM defaults, and with which prompts.
type HostConfig struct {
	// Version pins the schema. Only `1` is currently accepted.
	Version int

	// Container is the runtime environment all roles in this host
	// share. One container shape per host repo; per-role container
	// overrides land in a later milestone.
	Container Container

	// LLM is the team-default LLM configuration. nil means "fall
	// through to admin-configured platform default". Per-role
	// overrides on Role.LLM win when set.
	LLM *LLMConfig

	// Roles maps role-key → Role. The map is guaranteed non-empty by
	// the parser; an empty `roles:` is a misconfiguration. Iteration
	// order is not stable — callers that need deterministic order
	// (audit, etc.) must sort keys themselves.
	Roles map[string]*Role
}

// Container describes the runtime image and the per-host environment
// the runner mounts into every role's container. Exactly one of Image
// or Build is set; the parser rejects both-set and neither-set.
type Container struct {
	// Image is a fully-qualified registry pull spec
	// (`ghcr.io/acme/dev:1.2.3`). The platform pulls and caches.
	Image string

	// Build is the in-tree build alternative; set when the host
	// repo ships its own Dockerfile.
	Build *Build

	// Env is the plain-text env-var map injected into every role
	// container. Keys are uppercase `[A-Z_][A-Z0-9_]*`. Values may
	// be any string — including JSON / shell-quoted blobs the agent
	// will parse itself.
	Env map[string]string

	// Secrets is the name-only list of secrets the platform should
	// inject from the repo's "secrets" settings page. Values never
	// appear in this file (or in git); they're fetched at task-
	// claim time and dropped into the container env.
	Secrets []string

	// Volumes are repo-scoped named caches the runner binds into
	// the container. Order matters only for human review — the
	// runner mounts all of them.
	Volumes []Volume
}

// Build is the in-tree image-build alternative to a pull spec.
type Build struct {
	// Dockerfile is the path (repo-relative) to the Dockerfile.
	// Conventionally `.hangrix/agent.Dockerfile`.
	Dockerfile string

	// Context is the build context root, repo-relative. `.` means
	// repo root.
	Context string

	// Args is the build-arg map passed to `docker build --build-arg`.
	// String values only — bool / int are rendered by yaml as their
	// string form and pass through unchanged.
	Args map[string]string
}

// Volume is one named cache mount.
type Volume struct {
	// Name is the platform-scoped identifier (e.g. `pnpm-store`).
	// Two volumes with the same name share the same host directory;
	// the runner namespaces by repo id automatically.
	Name string

	// Mount is the absolute path inside the container. Parser rejects
	// non-absolute paths and any `..` segments.
	Mount string
}

// LLMConfig is the LLM tuning block. It appears twice in the host yaml
// (team default and per-role override); the same shape backs both. The
// parser refuses an empty Model — an LLM block with no model is a
// misconfiguration, not "use defaults".
type LLMConfig struct {
	// Model is a name string the LLM provider's allowed_models list
	// must contain at runtime. This package does not resolve the
	// model against any registry — that lookup belongs in the
	// llm_provider module.
	Model string

	// MaxOutputTokens caps the per-call output budget (Anthropic
	// `max_tokens`, OpenAI `max_output_tokens`). 0 means "let the
	// upstream apply its default"; negative is rejected by the parser.
	MaxOutputTokens int

	// MaxContextTokens caps the prompt+history window the agent will
	// pack before truncation. 0 means "no cap declared here — the
	// agent / proxy applies its own ceiling". Negative is rejected.
	// The agent runtime enforces this; the LLM proxy does not.
	MaxContextTokens int

	// ReasoningEffort mirrors OpenAI's `reasoning.effort` enum and
	// drives Anthropic `thinking.budget_tokens` translation in the
	// proxy. Allowed values: "" (unset, upstream default) /
	// "minimal" / "low" / "medium" / "high". Adapters without a
	// native reasoning knob ignore the field.
	ReasoningEffort string

	// Temperature must be in [0.0, 2.0]. The zero value is a
	// legitimate setting (deterministic decoding) and indistinguishable
	// from "not set" — callers that need that distinction should rely
	// on the per-role / team-default override chain instead of probing
	// for zero.
	Temperature float64

	// TopP must be in [0.0, 1.0]. Same zero-value caveat as Temperature.
	TopP float64
}

// ValidReasoningEfforts is the closed set of values accepted on
// `llm.reasoning_effort`. The empty string is the unset sentinel and is
// also accepted by the parser; it is not listed here because the
// validator treats "" as "field omitted".
var ValidReasoningEfforts = []string{"minimal", "low", "medium", "high"}

// IsValidReasoningEffort reports whether s is "" (unset) or one of the
// canonical OpenAI reasoning.effort values.
func IsValidReasoningEffort(s string) bool {
	if s == "" {
		return true
	}
	for _, v := range ValidReasoningEfforts {
		if s == v {
			return true
		}
	}
	return false
}
