package agentsconfig

import (
	"errors"
	"reflect"
	"testing"
)

// goldenHost is a TEAM-ONLY `.hangrix/agents.yml`: version, container,
// team llm, and the reusable `tools:` rule map. Roles no longer live
// here — they are one Markdown file each under `.hangrix/agents/` and
// are parsed by ParseAgentFile / assembled by LoadHostConfig.
const goldenHost = `
version: 1

container:
  image: ghcr.io/acme/dev:1.2.3
  env:
    NODE_ENV: development
    GOFLAGS: "-mod=readonly"
  volumes:
    - { name: pnpm-store, mount: /caches/pnpm }
    - { name: go-mod,    mount: /go/pkg/mod }

llm:
  model: claude-sonnet-4-6

tools:
  all: ["*"]
  reviewer: [issue_read, issue_comment, issue_review_vote]
`

func TestParseHostConfig_Happy(t *testing.T) {
	t.Parallel()

	cfg, err := ParseHostConfig([]byte(goldenHost))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("version: %d", cfg.Version)
	}
	if cfg.Container.Image != "ghcr.io/acme/dev:1.2.3" {
		t.Fatalf("image: %q", cfg.Container.Image)
	}
	if cfg.Container.Build != nil {
		t.Fatalf("expected no build, got %+v", cfg.Container.Build)
	}
	if cfg.Container.Env["NODE_ENV"] != "development" {
		t.Fatalf("env NODE_ENV: %+v", cfg.Container.Env)
	}
	if len(cfg.Container.Volumes) != 2 || cfg.Container.Volumes[0].Mount != "/caches/pnpm" {
		t.Fatalf("volumes: %+v", cfg.Container.Volumes)
	}
	if cfg.LLM == nil || cfg.LLM.Model != "claude-sonnet-4-6" {
		t.Fatalf("team llm: %+v", cfg.LLM)
	}

	// Tool rules: the `tools:` map is lifted verbatim (name → glob list).
	if cfg.Tools == nil {
		t.Fatalf("expected tool rules, got nil")
	}
	if want := []string{"*"}; !reflect.DeepEqual(cfg.Tools["all"], want) {
		t.Fatalf("tools.all = %v, want %v", cfg.Tools["all"], want)
	}
	if want := []string{"issue_read", "issue_comment", "issue_review_vote"}; !reflect.DeepEqual(cfg.Tools["reviewer"], want) {
		t.Fatalf("tools.reviewer = %v, want %v", cfg.Tools["reviewer"], want)
	}

	// ParseHostConfig parses team config only; roles live in
	// `.hangrix/agents/<role>.md` and are populated by LoadHostConfig.
	if cfg.Roles != nil {
		t.Fatalf("ParseHostConfig must not populate roles, got %v", cfg.Roles)
	}
}

func TestParseHostConfig_Entrypoint(t *testing.T) {
	t.Parallel()

	body := `
version: 1
container:
  image: x
  entrypoint: ["/init"]
`
	cfg, err := ParseHostConfig([]byte(body))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got, want := cfg.Container.Entrypoint, []string{"/init"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("entrypoint: %v", got)
	}
}

func TestParseHostConfig_EntrypointOmittedDefaultsToNil(t *testing.T) {
	t.Parallel()

	body := `
version: 1
container: { image: x }
`
	cfg, err := ParseHostConfig([]byte(body))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Container.Entrypoint != nil {
		t.Fatalf("entrypoint should be nil when omitted, got %v", cfg.Container.Entrypoint)
	}
}

func TestParseHostConfig_Build(t *testing.T) {
	t.Parallel()

	body := `
version: 1
container:
  build:
    dockerfile: .hangrix/agent.Dockerfile
    context: .
    args: { GO_VERSION: "1.26" }
`
	cfg, err := ParseHostConfig([]byte(body))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Container.Build == nil || cfg.Container.Build.Dockerfile != ".hangrix/agent.Dockerfile" {
		t.Fatalf("build: %+v", cfg.Container.Build)
	}
	if cfg.Container.Build.Args["GO_VERSION"] != "1.26" {
		t.Fatalf("build args: %+v", cfg.Container.Build.Args)
	}
}

// TestParseHostConfig_Errors covers the team-level invariants
// ParseHostConfig still enforces. Role-shaped invariants (triggers,
// prompt, permission, duplicate/invalid role keys, mcp) moved to
// ParseAgentFile / LoadHostConfig and are exercised in load_test.go.
func TestParseHostConfig_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		body   string
		target error
	}{
		{
			name: "bad-version",
			body: `
version: 2
container: { image: x }
`,
			target: ErrInvalidVersion,
		},
		{
			name: "missing-container",
			body: `
version: 1
`,
			target: ErrContainerSourceConflict,
		},
		{
			name: "image-and-build",
			body: `
version: 1
container:
  image: x
  build:
    dockerfile: D
`,
			target: ErrContainerSourceConflict,
		},
		{
			name: "neither-image-nor-build",
			body: `
version: 1
container: { env: { FOO: bar } }
`,
			target: ErrContainerSourceConflict,
		},
		{
			name: "bad-env-key-lower",
			body: `
version: 1
container:
  image: x
  env: { node_env: 1 }
`,
			target: ErrInvalidEnvKey,
		},
		{
			name: "bad-volume-mount-relative",
			body: `
version: 1
container:
  image: x
  volumes:
    - { name: cache, mount: caches/foo }
`,
			target: ErrInvalidVolumeMount,
		},
		{
			name: "bad-volume-mount-escape",
			body: `
version: 1
container:
  image: x
  volumes:
    - { name: cache, mount: /caches/../../etc }
`,
			target: ErrInvalidVolumeMount,
		},
		{
			name: "empty-volume-name",
			body: `
version: 1
container:
  image: x
  volumes:
    - { name: "", mount: /caches/x }
`,
			target: ErrInvalidVolumeMount,
		},
		{
			name: "bad-llm-temp",
			body: `
version: 1
container: { image: x }
llm:
  model: m
  temperature: 5
`,
			target: ErrInvalidLLMParam,
		},
		{
			name: "bad-llm-topp",
			body: `
version: 1
container: { image: x }
llm:
  model: m
  top_p: 2
`,
			target: ErrInvalidLLMParam,
		},
		{
			name: "bad-llm-max-output-tokens",
			body: `
version: 1
container: { image: x }
llm:
  model: m
  max_output_tokens: -1
`,
			target: ErrInvalidLLMParam,
		},
		{
			name: "bad-llm-max-context-tokens",
			body: `
version: 1
container: { image: x }
llm:
  model: m
  max_context_tokens: -1
`,
			target: ErrInvalidLLMParam,
		},
		{
			name: "llm-missing-model",
			body: `
version: 1
container: { image: x }
llm: { max_output_tokens: 100 }
`,
			target: ErrInvalidModel,
		},
		{
			name: "entrypoint-empty-element",
			body: `
version: 1
container:
  image: x
  entrypoint: ["/init", ""]
`,
			target: ErrInvalidContainerEntrypoint,
		},
		{
			// A `tools:` rule with an empty glob pattern is rejected.
			name: "bad-tool-rule",
			body: `
version: 1
container: { image: x }
tools:
  broken: [""]
`,
			target: ErrInvalidToolRule,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseHostConfig([]byte(tc.body))
			if err == nil {
				t.Fatalf("expected err, got nil")
			}
			if !errors.Is(err, tc.target) {
				t.Fatalf("got %v, want errors.Is %v", err, tc.target)
			}
		})
	}
}

// TestParseHostConfig_IgnoresUnknownFields pins the forward-compat
// contract: unknown keys at the top level and inside the container block
// are silently dropped rather than rejected, so a host shipping newer
// fields against an older agent server doesn't brick. (Role-body and
// per-trigger forward-compat is exercised at the ParseAgentFile layer.)
func TestParseHostConfig_IgnoresUnknownFields(t *testing.T) {
	t.Parallel()

	body := `
version: 1
weird_top_level: 42
container:
  image: x
  weird_container_field: ignore-me
`
	cfg, err := ParseHostConfig([]byte(body))
	if err != nil {
		t.Fatalf("unknown fields should be ignored, got err: %v", err)
	}
	if cfg.Container.Image != "x" {
		t.Fatalf("image: %q", cfg.Container.Image)
	}
}
