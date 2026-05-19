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

	// Entrypoint overrides the container's PID 1. First element is
	// the executable; subsequent elements are appended after the
	// image name in `docker create`. Empty / nil = the runner uses
	// its built-in default (`/usr/bin/sleep infinity`) so the
	// container is a passive sandbox for `docker exec`. Set this to
	// `[/init]` (s6-overlay) or any other supervisor when you want
	// the image to bring up background services automatically.
	Entrypoint []string

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
// (team default and per-role override); the same shape backs both, with
// one asymmetry: at team level the parser refuses an empty Model (an
// llm block declared with no model is a misconfiguration), at role
// level Model MAY be empty meaning "inherit from team".
//
// Every scalar field except Model is a pointer so the merge step can
// distinguish "field omitted, inherit team value" (nil) from "field
// explicitly set, override team" (non-nil) — including legitimate zero
// values like `temperature: 0`. Field-level merge happens at session
// spawn time in modules/agent_session/service.resolveLLM.
type LLMConfig struct {
	// Model is a name string the LLM provider's allowed_models list
	// must contain at runtime. Empty at role level = inherit team's
	// model; empty at team level is rejected by the parser.
	Model string

	// MaxOutputTokens caps the per-call output budget (Anthropic
	// `max_tokens`, OpenAI `max_output_tokens`). nil = inherit (or
	// "let the upstream apply its default" at the bottom of the
	// chain); 0 has the same operational meaning as nil but the
	// pointer lets a role explicitly reset team's non-zero default.
	// Negative is rejected by the parser.
	MaxOutputTokens *int

	// MaxContextTokens caps the prompt+history window the agent will
	// pack before truncation. nil = inherit (or "no cap" at the
	// bottom of the chain). Negative is rejected. The agent runtime
	// enforces this; the LLM proxy does not.
	MaxContextTokens *int

	// ReasoningEffort is passed through to the upstream as the
	// `reasoning.effort` / equivalent thinking knob. nil = inherit.
	// Canonical values "minimal" / "low" / "medium" / "high" drive
	// the Anthropic adapter's `thinking.budget_tokens` translation;
	// any other non-empty string is forwarded verbatim to the
	// upstream. Pointer-to-empty-string explicitly resets a team
	// default back to "don't send reasoning to upstream".
	ReasoningEffort *string

	// Temperature must be in [0.0, 2.0]. nil = inherit (or "upstream
	// default" at the bottom of the chain). The pointer lets a role
	// legitimately request `temperature: 0` (deterministic decoding)
	// without that being mistaken for "field omitted".
	Temperature *float64

	// TopP must be in [0.0, 1.0]. Same nil-as-inherit semantics as
	// Temperature.
	TopP *float64
}

