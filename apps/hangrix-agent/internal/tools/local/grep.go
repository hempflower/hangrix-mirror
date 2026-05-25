package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// grep prefers ripgrep when available — it's an order of magnitude faster
// than walking + regex'ing in pure Go and is .gitignore-aware out of the
// box, both of which matter when an agent runs `grep "TODO" .` on a real
// repo. The Go fallback mirrors ripgrep's behaviour: .git/ is skipped and
// .gitignore rules are honoured via the go-gitignore library already used
// by the glob tool in this package.

type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	IgnoreCase bool   `json:"ignore_case"`
	Glob       string `json:"glob"` // limit to files matching this pattern, e.g. "*.go"
	Limit      int    `json:"limit"`
}

type grepTool struct {
	rgPath string // empty if rg not on PATH
}

func newGrepTool() Tool {
	t := &grepTool{}
	if p, err := exec.LookPath("rg"); err == nil {
		t.rgPath = p
	}
	return t
}

func (grepTool) Name() string { return "grep" }
func (grepTool) Description() string {
	return "Search for a regular expression across files. Uses ripgrep when available (.gitignore-aware). Returns matches as 'path:lineno:line'."
}
func (grepTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern":     map[string]any{"type": "string"},
			"path":        map[string]any{"type": "string", "description": "Directory or file to search. Default \".\"."},
			"ignore_case": map[string]any{"type": "boolean"},
			"glob":        map[string]any{"type": "string", "description": "Restrict matches to files whose name matches this glob (e.g. \"*.go\")."},
			"limit":       map[string]any{"type": "integer", "description": "Maximum number of matching lines to return. Default 200."},
		},
		"required": []string{"pattern"},
	}
}

func (g *grepTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	var a grepArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.Pattern == "" {
		return nil, errors.New("grep: missing required 'pattern' argument. Provide an RE2 regular expression (the same syntax Go and ripgrep use) — set 'ignore_case' for case-insensitive matching.")
	}
	if a.Path == "" {
		a.Path = "."
	}
	if a.Limit <= 0 {
		a.Limit = 200
	}

	if g.rgPath != "" {
		return g.runRipgrep(ctx, a)
	}
	return g.runGoFallback(a)
}

func (g *grepTool) runRipgrep(ctx context.Context, a grepArgs) (any, error) {
	args := []string{"--no-heading", "--with-filename", "--line-number", "--color=never"}
	if a.IgnoreCase {
		args = append(args, "--ignore-case")
	}
	if a.Glob != "" {
		args = append(args, "--glob", a.Glob)
	}
	args = append(args, "--", a.Pattern, a.Path)
	cmd := exec.CommandContext(ctx, g.rgPath, args...)
	out, err := cmd.Output()
	// ripgrep returns exit 1 when there are zero matches — that's not an
	// error from the agent's perspective; report empty results instead.
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return map[string]any{"pattern": a.Pattern, "count": 0, "matches": []string{}}, nil
		}
		return nil, fmt.Errorf("grep (ripgrep): %w. If the pattern is invalid, re-check it as an RE2 regex; if the path is wrong, use 'glob' to discover files first.", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > a.Limit {
		lines = lines[:a.Limit]
	}
	return map[string]any{
		"pattern":   a.Pattern,
		"count":     len(lines),
		"matches":   lines,
		"truncated": len(lines) >= a.Limit,
	}, nil
}

func (g *grepTool) runGoFallback(a grepArgs) (any, error) {
	flags := ""
	if a.IgnoreCase {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + a.Pattern)
	if err != nil {
		return nil, fmt.Errorf("grep: pattern %q is not a valid RE2 regex: %w. RE2 omits backreferences and lookarounds — rewrite without those features, or escape literal regex metacharacters (\\., \\*, \\(, etc).", a.Pattern, err)
	}
	var matches []string
	ig := loadGitignore()
	root, _ := filepath.Abs(a.Path)

	walkErr := filepath.WalkDir(a.Path, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// Never descend into .git/ unless it was the explicit search root.
		if d.IsDir() && d.Name() == ".git" && path != root {
			return filepath.SkipDir
		}
		// Respect .gitignore — skip ignored directories entirely
		// so we don't waste time reading their contents.
		if pathIgnored(ig, path, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if a.Glob != "" {
			ok, _ := filepath.Match(a.Glob, filepath.Base(path))
			if !ok {
				return nil
			}
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(body), "\n") {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", path, i+1, line))
				if len(matches) >= a.Limit {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		return nil, walkErr
	}
	return map[string]any{
		"pattern":   a.Pattern,
		"count":     len(matches),
		"matches":   matches,
		"truncated": len(matches) >= a.Limit,
	}, nil
}
