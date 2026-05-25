package agentsconfig

import (
	"fmt"
	"regexp"

	"go.yaml.in/yaml/v3"
)

// AutomationConfig models the parsed `.hangrix/automation.yml`.
type AutomationConfig struct {
	Version int     `yaml:"version"`
	Tasks   []*Task `yaml:"tasks"`
}

// Task is a single scheduled automation task definition.
type Task struct {
	Name     string    `yaml:"name"`
	Schedule string    `yaml:"schedule"`
	Issue    IssueSpec `yaml:"issue"`
	Roles    []string  `yaml:"roles"`
	Enabled  bool      `yaml:"enabled"`
}

// IssueSpec is the issue-creation template within a task.
type IssueSpec struct {
	Title  string   `yaml:"title"`
	Body   string   `yaml:"body"`
	Labels []string `yaml:"labels"`
}

// AutomationConfigValidationError is returned when the automation config
// fails validation. It collects all field-level errors so the caller can
// surface them in one response.
type AutomationConfigValidationError struct {
	Errors []string
}

func (e *AutomationConfigValidationError) Error() string {
	return fmt.Sprintf("automation config validation: %v", e.Errors)
}

// taskNameRe is the canonical regex for automation task names.
var taskNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ParseAutomationConfig parses raw YAML bytes into an AutomationConfig.
// Returns an error for invalid YAML syntax; validation is a separate step.
func ParseAutomationConfig(raw []byte) (*AutomationConfig, error) {
	var cfg AutomationConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse automation.yml: %w", err)
	}
	return &cfg, nil
}

// Validate checks the parsed config against the schema rules. Returns nil
// on success, or an *AutomationConfigValidationError with all issues.
func (c *AutomationConfig) Validate() error {
	var errs []string

	if c.Version != 1 {
		errs = append(errs, "version must be 1")
	}

	seen := make(map[string]bool, len(c.Tasks))
	for i, t := range c.Tasks {
		prefix := fmt.Sprintf("tasks[%d]", i)
		if t == nil {
			errs = append(errs, prefix+": is nil")
			continue
		}

		if t.Name == "" {
			errs = append(errs, prefix+".name: required")
		} else if len(t.Name) > 100 {
			errs = append(errs, prefix+".name: max 100 characters")
		} else if !taskNameRe.MatchString(t.Name) {
			errs = append(errs, prefix+".name: must match [a-z][a-z0-9-]*")
		}

		if seen[t.Name] {
			errs = append(errs, prefix+".name: duplicate")
		}
		seen[t.Name] = true

		if t.Schedule == "" {
			errs = append(errs, prefix+".schedule: required")
		}

		if t.Issue.Title == "" {
			errs = append(errs, prefix+".issue.title: required")
		} else if len(t.Issue.Title) > 200 {
			errs = append(errs, prefix+".issue.title: max 200 characters")
		}

		if len(t.Roles) == 0 {
			errs = append(errs, prefix+".roles: at least one role required")
		}
	}

	if len(errs) > 0 {
		return &AutomationConfigValidationError{Errors: errs}
	}
	return nil
}
