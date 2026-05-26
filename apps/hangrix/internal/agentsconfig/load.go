package agentsconfig

import (
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"
)

// HostConfigPath is the canonical path of the team-level config file.
const HostConfigPath = ".hangrix/agents.yml"

// AgentsDir is the directory holding one Markdown file per role. Each
// `<role-key>.md` carries the role's config in YAML front matter and its
// prompt in the Markdown body.
const AgentsDir = ".hangrix/agents"

// utf8BOM is stripped from the head of an agent file before parsing.
const utf8BOM = "\ufeff"

// FileProvider abstracts reading the host config files out of a repo at a
// fixed ref. Implementations bind a (repo, ref) and expose plain path
// access so the agentsconfig package stays free of git/module deps.
type FileProvider interface {
	// ReadFile returns the bytes at the repo-relative path. ok is false
	// when the path does not exist at the bound ref.
	ReadFile(path string) ([]byte, bool)
	// ListDir returns the repo-relative paths of the entries directly
	// under dir. ok is false when dir does not exist.
	ListDir(dir string) ([]string, bool)
}

// LoadHostConfig assembles the full host config from `.hangrix/agents.yml`
// (team config + tool rules) plus every `.hangrix/agents/<role>.md` file
// (one role each). Returns (nil, nil) when agents.yml is absent — a repo
// with no agent team is a valid state. The returned config is normalized
// by the caller via NormalizeHostConfig if defaults are needed.
func LoadHostConfig(fp FileProvider) (*HostConfig, error) {
	body, ok := fp.ReadFile(HostConfigPath)
	if !ok {
		return nil, nil
	}
	host, err := ParseHostConfig(body)
	if err != nil {
		return nil, err
	}

	roles := make(map[string]*Role)
	if entries, ok := fp.ListDir(AgentsDir); ok {
		for _, p := range entries {
			if !strings.HasSuffix(p, ".md") {
				continue
			}
			key := strings.TrimSuffix(path.Base(p), ".md")
			md, ok := fp.ReadFile(p)
			if !ok {
				continue
			}
			role, err := ParseAgentFile(key, md)
			if err != nil {
				return nil, err
			}
			if _, dup := roles[key]; dup {
				return nil, fmt.Errorf("%w: %q", ErrDuplicateRoleKey, key)
			}
			roles[key] = role
		}
	}

	if err := AssembleHostConfig(host, roles); err != nil {
		return nil, err
	}
	return host, nil
}

// ParseAgentFile parses one `.hangrix/agents/<role>.md` file. roleKey is
// the filename without the `.md` suffix. The YAML front matter (delimited
// by `---` fences) supplies the role config; the Markdown body after the
// closing fence is the role's prompt. ToolPatterns is left nil here and
// resolved in AssembleHostConfig where the rule map is available.
func ParseAgentFile(roleKey string, body []byte) (*Role, error) {
	if !isValidRoleKey(roleKey) {
		return nil, fmt.Errorf("%w: %q is not a valid role key", ErrInvalidAgentFile, roleKey)
	}
	fm, prompt, err := splitFrontMatter(body)
	if err != nil {
		return nil, fmt.Errorf("agent %q: %w", roleKey, err)
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("%w: agent %q has an empty prompt body", ErrInvalidAgentFile, roleKey)
	}

	var w roleWire
	dec := yaml.NewDecoder(strings.NewReader(fm))
	if err := dec.Decode(&w); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: agent %q has empty front matter", ErrInvalidAgentFile, roleKey)
		}
		return nil, fmt.Errorf("%w: agent %q front matter: %s", ErrInvalidAgentFile, roleKey, err.Error())
	}

	role, err := buildRole(roleKey, &w)
	if err != nil {
		return nil, err
	}
	role.Prompt = prompt
	return role, nil
}

// splitFrontMatter separates a Markdown file into its YAML front matter
// and the body that follows the closing fence. The file must open with a
// `---` line (leading blank lines tolerated) and the front matter must be
// terminated by a second `---` line. Returns the front-matter YAML and the
// trimmed body.
func splitFrontMatter(body []byte) (frontMatter, prompt string, err error) {
	s := strings.TrimPrefix(string(body), utf8BOM)
	lines := strings.Split(s, "\n")

	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) || strings.TrimSpace(lines[i]) != "---" {
		return "", "", fmt.Errorf("%w: file must start with a '---' front-matter fence", ErrInvalidAgentFile)
	}
	start := i + 1

	end := -1
	for j := start; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "---" {
			end = j
			break
		}
	}
	if end < 0 {
		return "", "", fmt.Errorf("%w: unterminated front matter (no closing '---')", ErrInvalidAgentFile)
	}

	frontMatter = strings.Join(lines[start:end], "\n")
	prompt = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return frontMatter, prompt, nil
}

// AssembleHostConfig finalizes a host config: it requires at least one
// role, resolves each role's `tools:` rule references into ToolPatterns
// (the union of the named rules' glob patterns), and validates the
// reviewer block against the now-known roles. Mutates host in place.
func AssembleHostConfig(host *HostConfig, roles map[string]*Role) error {
	if len(roles) == 0 {
		return ErrEmptyRoles
	}
	for key, role := range roles {
		patterns, err := resolveToolPatterns(key, role.Tools, host.Tools)
		if err != nil {
			return err
		}
		role.ToolPatterns = patterns
	}
	host.Roles = roles
	return validateReviewers(host.Reviewers, roles)
}

// resolveToolPatterns expands a role's referenced rule names into the
// deduplicated union of their glob patterns. Every referenced rule must
// exist in the host's `tools:` map.
func resolveToolPatterns(roleKey string, refs []string, rules map[string][]string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, ref := range refs {
		patterns, ok := rules[ref]
		if !ok {
			return nil, fmt.Errorf("%w: role %q references undefined tool rule %q", ErrInvalidToolRule, roleKey, ref)
		}
		for _, p := range patterns {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// matchesAnyGlob reports whether name matches at least one of the glob
// patterns (shell-style `*`; tool names contain no `/` so path.Match's
// separator handling is irrelevant). An exact-string pattern matches
// verbatim.
func matchesAnyGlob(patterns []string, name string) bool {
	for _, p := range patterns {
		if p == name {
			return true
		}
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}
