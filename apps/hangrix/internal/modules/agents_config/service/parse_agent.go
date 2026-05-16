package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agents_config/domain"
)

// agentWire mirrors the on-disk YAML shape verbatim. Decoder will reject
// any key outside this set via KnownFields(true) — that single check
// shoulders the "agent.yml must not contain host fields" guarantee
// because container / env / secrets / volumes / llm / roles simply
// aren't here.
//
// Forbidden host-side fields are flagged explicitly (`forbiddenWire`) so
// the error message can name them rather than letting yaml print the
// generic "field not found" text — operators benefit from being told
// where a misplaced key actually belongs.
type agentWire struct {
	Version        int              `yaml:"version"`
	Kind           string           `yaml:"kind"`
	Entry          agentEntryWire   `yaml:"entry"`
	DeclaredTools  []string         `yaml:"declared_tools"`
}

type agentEntryWire struct {
	BasePrompt string `yaml:"base_prompt"`
}

// hostOnlyFields enumerates the keys that legitimately appear in the
// host yaml but must NEVER appear in the agent yaml. Caught with a
// pre-decode scan so the error blames the right schema rather than
// surfacing as "unknown field".
var hostOnlyFields = []string{
	"container",
	"image",
	"build",
	"env",
	"secrets",
	"volumes",
	"llm",
	"roles",
}

// ParseAgentManifest decodes an agent.yml body and validates it.
//
// Validation rules (mirrored from docs/agent-config.md):
//
//   - version == 1
//   - kind == "agent"
//   - entry.base_prompt non-empty AND a relative repo-root-anchored path
//     (no leading `/`, no `..` segments).
//   - declared_tools entries are non-empty slugs (lowercase letters,
//     digits, underscores; same shape the platform uses for permission
//     identifiers).
//   - No host-only keys at top level (image / build / container / env /
//     secrets / volumes / llm / roles).
//   - No unknown top-level keys at all (yaml.v3 KnownFields(true)).
func ParseAgentManifest(body []byte) (*domain.AgentManifest, error) {
	if err := rejectAgentSchemaHostFields(body); err != nil {
		return nil, err
	}

	var wire agentWire
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&wire); err != nil {
		// io.EOF on empty input deserves a clearer message than
		// yaml's bare "EOF" so the operator knows their file is
		// empty rather than truncated mid-decode.
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("agent.yml is empty")
		}
		return nil, fmt.Errorf("%w: %s", domain.ErrUnknownField, err.Error())
	}

	if wire.Version != 1 {
		return nil, fmt.Errorf("%w: got %d, want 1", domain.ErrInvalidVersion, wire.Version)
	}
	if wire.Kind != "agent" {
		return nil, fmt.Errorf("%w: got %q, want %q", domain.ErrInvalidKind, wire.Kind, "agent")
	}

	if err := validateRelativePromptPath(wire.Entry.BasePrompt); err != nil {
		return nil, err
	}

	for i, tool := range wire.DeclaredTools {
		if !isValidToolSlug(tool) {
			return nil, fmt.Errorf("%w: declared_tools[%d]=%q", domain.ErrInvalidDeclaredTool, i, tool)
		}
	}

	return &domain.AgentManifest{
		Version:       wire.Version,
		Kind:          wire.Kind,
		Entry:         domain.Entry{BasePrompt: wire.Entry.BasePrompt},
		DeclaredTools: wire.DeclaredTools,
	}, nil
}

// rejectAgentSchemaHostFields scans the top-level keys before yaml's
// KnownFields kicks in, so a mistakenly-pasted `container:` block
// produces a specific "host-only field" error rather than a generic
// schema rejection. A decode-then-check approach would require a
// permissive structural pass which would let other typos through.
func rejectAgentSchemaHostFields(body []byte) error {
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(body, &raw); err != nil {
		// Leave the strict decode below to produce the canonical
		// error — a parse failure here would also fail there with
		// a better message.
		return nil
	}
	for _, k := range hostOnlyFields {
		if _, present := raw[k]; present {
			return fmt.Errorf("%w: %q belongs in .hangrix/agents.yml", domain.ErrAgentSchemaForbiddenField, k)
		}
	}
	return nil
}

// validateRelativePromptPath enforces "non-empty, relative, no escape".
// path.Clean collapses `./` and double slashes — after which any leading
// `/` or any `..` segment is a violation.
func validateRelativePromptPath(p string) error {
	if p == "" {
		return domain.ErrMissingBasePrompt
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: %q is absolute", domain.ErrInvalidBasePromptPath, p)
	}
	cleaned := path.Clean(p)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%w: %q escapes repo root", domain.ErrInvalidBasePromptPath, p)
	}
	if cleaned == "." {
		return fmt.Errorf("%w: %q resolves to repo root", domain.ErrInvalidBasePromptPath, p)
	}
	return nil
}

// isValidToolSlug matches `^[a-z][a-z0-9_]*$`. Slugs are the platform's
// tool-identifier shape (`issue_read`, `issue_review_vote`); enforcing
// it at parse time catches typos like `Issue-Read` early.
func isValidToolSlug(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		case r == '_' && i > 0:
		default:
			return false
		}
	}
	return true
}

