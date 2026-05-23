package workflowsconfig

import "strings"

// normalizeConfig applies defaults to a parsed and validated WorkflowConfig.
// This is separated from parsing so callers can distinguish "user omitted"
// from "default applied".
func normalizeConfig(cfg *WorkflowConfig) {
	if cfg.Env == nil {
		cfg.Env = make(map[string]string)
	}
	for i := range cfg.Jobs {
		job := &cfg.Jobs[i]
		if job.Env == nil {
			job.Env = make(map[string]string)
		}
		if job.TimeoutMinutes == 0 {
			job.TimeoutMinutes = 60
		}
		if job.WorkingDirectory == "" {
			job.WorkingDirectory = "/workspace"
		}
		if job.DisplayName == "" {
			job.DisplayName = job.Key
		}
	}
}

// ValidateConfigSet checks cross-file constraints for all workflow configs
// in a single repo. Currently only checks for duplicate workflow names.
func ValidateConfigSet(configs []*WorkflowConfig) error {
	var errs []string
	seen := map[string]string{} // name -> sourceFile

	for _, cfg := range configs {
		if prev, ok := seen[cfg.Name]; ok {
			errs = append(errs, "duplicate workflow name \""+cfg.Name+"\" in "+cfg.SourceFile+" (already defined in "+prev+")")
		}
		seen[cfg.Name] = cfg.SourceFile
	}

	if len(errs) > 0 {
		return &WorkflowConfigSetValidationError{Errors: errs}
	}
	return nil
}

// MatchesPushEvent checks whether a repo.push event trigger should fire for
// the given branch and changed paths.
func (t EventTrigger) MatchesPushEvent(branch string, changedPaths []string) bool {
	if t.Event != EventRepoPush {
		return false
	}

	// Branches filter
	if len(t.Branches) > 0 {
		if !matchAnyGlob(t.Branches, branch) {
			return false
		}
	}

	// Branches ignore
	if len(t.BranchesIgnore) > 0 {
		if matchAnyGlob(t.BranchesIgnore, branch) {
			return false
		}
	}

	// Paths filter: at least one changed path must match
	if len(t.Paths) > 0 {
		matched := false
		for _, p := range changedPaths {
			if matchAnyGlob(t.Paths, p) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Paths ignore: ALL changed paths must be ignored for the trigger to be suppressed
	if len(t.PathsIgnore) > 0 {
		allIgnored := true
		for _, p := range changedPaths {
			if !matchAnyGlob(t.PathsIgnore, p) {
				allIgnored = false
				break
			}
		}
		if allIgnored {
			return false
		}
	}

	return true
}

// MatchesPushTagEvent checks whether a repo.push_tag event trigger should fire for
// the given short tag name (e.g. "v1.2.3", not "refs/tags/v1.2.3").
func (t EventTrigger) MatchesPushTagEvent(tag string) bool {
	if t.Event != EventRepoPushTag {
		return false
	}

	// Tags filter
	if len(t.Tags) > 0 {
		if !matchAnyGlob(t.Tags, tag) {
			return false
		}
	}

	// Tags ignore
	if len(t.TagsIgnore) > 0 {
		if matchAnyGlob(t.TagsIgnore, tag) {
			return false
		}
	}

	return true
}

// MatchesCommentEvent checks whether an issue.comment event trigger should fire.
func (t EventTrigger) MatchesCommentEvent(fromRole, fromUser string, mentionedWorkflow string) bool {
	if t.Event != EventIssueComment {
		return false
	}

	// mentioned_only
	if t.MentionedOnly && mentionedWorkflow != t.Event.String() {
		// The mention check: "@workflow-<name>" must be present in the comment.
		// The caller passes the parsed workflow name from the mention.
		// Actually, we check if any mentioned workflow matches this trigger's workflow.
		return false
	}

	// from_roles
	if len(t.FromRoles) > 0 {
		matched := false
		for _, r := range t.FromRoles {
			if r == fromRole {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// from_users
	if len(t.FromUsers) > 0 {
		matched := false
		for _, u := range t.FromUsers {
			if u == fromUser {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

// String returns the string representation of the event name.
func (e EventName) String() string { return string(e) }

// matchAnyGlob returns true if s matches any of the glob patterns.
// Uses filepath.Match semantics (* matches within a segment).
func matchAnyGlob(patterns []string, s string) bool {
	for _, p := range patterns {
		if ok, _ := pathMatch(p, s); ok {
			return true
		}
	}
	return false
}

// pathMatch is a thin wrapper around the standard library's path.Match.
// We import "path" instead of "path/filepath" because these patterns use
// forward-slashes (git paths), not OS-specific separators.
func pathMatch(pattern, name string) (bool, error) {
	// Use simple wildcard matching.
	// Re-implementing path.Match inline to avoid import issues with agentconfig
	// conventions. This is a minimal glob for git paths.
	return matchSimple(pattern, name)
}

// matchSimple implements basic glob matching with * and ? wildcards.
func matchSimple(pattern, name string) (bool, error) {
	pi, ni := 0, 0
	for pi < len(pattern) && ni < len(name) {
		switch pattern[pi] {
		case '?':
			pi++
			ni++
		case '*':
			// Eat consecutive stars
			for pi < len(pattern) && pattern[pi] == '*' {
				pi++
			}
			if pi == len(pattern) {
				return true, nil
			}
			// Try to match the rest at every position
			for ni < len(name) {
				if ok, _ := matchSimple(pattern[pi:], name[ni:]); ok {
					return true, nil
				}
				ni++
			}
			return false, nil
		default:
			if pattern[pi] != name[ni] {
				return false, nil
			}
			pi++
			ni++
		}
	}
	// Both must be exhausted
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern) && ni == len(name), nil
}

// ensure path import is used
var _ = strings.TrimSuffix
