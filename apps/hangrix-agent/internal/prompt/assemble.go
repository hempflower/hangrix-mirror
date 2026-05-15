// Package prompt assembles the agent's system prompt from three layers:
//
//	<baseline.md>                     ← embedded into the binary
//	===== AGENT BASE PROMPT =====
//	<agent bundle's entry.base_prompt file>
//	===== HOST REPO ADDENDUM =====
//	<file at HANGRIX_HOST_ADDENDUM>
//
// Plus a runtime context block at the very top with role / branch /
// session metadata so the LLM has the immediate facts in the highest
// position the architecture allows.
//
// The agent.yml format is YAML, but we only need one path field
// (`entry.base_prompt`). Rather than pull a YAML dep, we scan the file
// line-by-line for that key — robust enough for a one-line value and
// keeps the binary stdlib-only. If parsing fails we log and skip the
// agent layer (rather than refuse to start) so a misconfigured bundle
// still allows a runnable agent for diagnostics.
package prompt

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed baseline.md
var baselineMD string

// Inputs is what Assemble needs from env / runtime. We thread it through
// a struct rather than ten arguments because the caller (runtime) wants
// to pass these through unchanged from os.Getenv reads in main.
type Inputs struct {
	// Bundle path (HANGRIX_AGENT_BUNDLE). Empty → skip agent layer.
	BundleDir string
	// Host addendum file (HANGRIX_HOST_ADDENDUM). Empty → skip host layer.
	HostAddendumPath string

	// Runtime context surfaced at the top of the prompt. Deliberately
	// excludes runner internals (LLM / MCP endpoints, credential
	// material) — the agent reaches those services through pre-wired
	// clients and does not need their addresses to operate.
	Role          string
	HostRepo      string
	IssueNumber   string
	WorkingBranch string
	BaseBranch    string
	SessionID     string
}

// Assembled bundles the final prompt with debug provenance the runtime
// can log. KeptLayers tells the caller which of {baseline, agent, host}
// actually contributed — a missing layer is intentional only if the
// caller meant for it to be missing.
type Assembled struct {
	Prompt     string
	KeptLayers []string
}

// Assemble produces the final system prompt. Errors are returned only
// for problems the operator should see (e.g. host addendum path set but
// unreadable); a missing-but-not-required layer is just dropped.
func Assemble(in Inputs) (*Assembled, error) {
	var (
		buf  strings.Builder
		kept []string
	)

	// (1) Runtime context. Plain key/value lines so the LLM can grep them
	// without us inventing a structured schema it has to parse.
	buf.WriteString("# Hangrix runtime context\n\n")
	writeKV(&buf, "role", in.Role)
	writeKV(&buf, "session_id", in.SessionID)
	writeKV(&buf, "host_repo", in.HostRepo)
	writeKV(&buf, "issue_number", in.IssueNumber)
	writeKV(&buf, "base_branch", in.BaseBranch)
	writeKV(&buf, "working_branch", in.WorkingBranch)
	buf.WriteString("\n")

	// (2) Baseline. Always present; it's compiled in.
	buf.WriteString(baselineMD)
	kept = append(kept, "baseline")

	// (3) Agent base prompt, read via the bundle's agent.yml.
	if in.BundleDir != "" {
		bp, err := loadBundleBasePrompt(in.BundleDir)
		if err != nil {
			// Surface as an error so a misconfigured bundle is loud — the
			// agent layer is the bulk of role identity, dropping it
			// silently would produce a generic agent that doesn't behave
			// like the role suggests.
			return nil, fmt.Errorf("prompt: read agent bundle %s: %w", in.BundleDir, err)
		}
		if bp != "" {
			buf.WriteString("\n\n===== AGENT BASE PROMPT =====\n\n")
			buf.WriteString(bp)
			kept = append(kept, "agent")
		}
	}

	// (4) Host addendum.
	if in.HostAddendumPath != "" {
		body, err := os.ReadFile(in.HostAddendumPath)
		if err != nil {
			return nil, fmt.Errorf("prompt: read host addendum %s: %w", in.HostAddendumPath, err)
		}
		if len(body) > 0 {
			buf.WriteString("\n\n===== HOST REPO ADDENDUM =====\n\n")
			buf.Write(body)
			kept = append(kept, "host")
		}
	}

	return &Assembled{Prompt: buf.String(), KeptLayers: kept}, nil
}

func writeKV(buf *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	buf.WriteString("- ")
	buf.WriteString(key)
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteByte('\n')
}

// loadBundleBasePrompt parses agent.yml in a deliberately minimal way:
// look for `entry:` followed by a `base_prompt:` line whose value is
// either bare text or a single-line scalar. This is enough for the
// Hangrix agent.yml schema (a small fixed set of keys); a richer YAML
// parser can replace it without churning callers if the schema grows.
func loadBundleBasePrompt(bundleDir string) (string, error) {
	yml := filepath.Join(bundleDir, "agent.yml")
	body, err := os.ReadFile(yml)
	if err != nil {
		return "", err
	}
	rel := scanEntryBasePrompt(string(body))
	if rel == "" {
		return "", nil
	}
	abs := rel
	if !filepath.IsAbs(rel) {
		abs = filepath.Join(bundleDir, rel)
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("base_prompt %s: %w", rel, err)
	}
	return string(content), nil
}

// scanEntryBasePrompt walks lines looking for a `base_prompt:` key
// indented under `entry:`. Returns "" if not found.
func scanEntryBasePrompt(yaml string) string {
	lines := strings.Split(yaml, "\n")
	inEntry := false
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if trimmed == "" {
			continue
		}
		// Top-level key (no leading whitespace + ends with ":")
		if !startsWithSpace(trimmed) {
			inEntry = strings.HasPrefix(trimmed, "entry:")
			continue
		}
		if !inEntry {
			continue
		}
		// Indented under entry: look for "base_prompt:"
		ws := strings.TrimLeft(trimmed, " \t")
		if strings.HasPrefix(ws, "base_prompt:") {
			val := strings.TrimSpace(strings.TrimPrefix(ws, "base_prompt:"))
			val = strings.Trim(val, `"'`)
			return val
		}
	}
	return ""
}

func startsWithSpace(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c == ' ' || c == '\t'
}
