// Package workflowsconfig parses and validates .hangrix/workflows/*.yml files.
// It follows the same pattern as agentsconfig: wire types with yaml tags for
// decoding, clean domain types for consumers, and strict validation.
package workflowsconfig

import "fmt"

// WorkflowConfig is a parsed workflow definition from a single
// .hangrix/workflows/*.yml file. This is the public domain type returned to
// callers after validation succeeds.
type WorkflowConfig struct {
	// Name is the unique workflow identifier within a repo.
	// Constraint: [a-z][a-z0-9-]*
	Name string

	// SourceFile is the relative path within .hangrix/workflows/, e.g. "ci.yml".
	// Informational only; Name is the stable identifier.
	SourceFile string

	// On lists the events that trigger this workflow. At least one required.
	On []EventTrigger

	// Env is the workflow-level env map. Optional.
	Env map[string]string

	// Jobs is the ordered job definitions. Non-empty; executed in declaration
	// order (v1 serial only).
	Jobs []JobDefinition

	// DispatchInputs carries the declared inputs when workflow.dispatch is
	// among the triggers. Nil/empty when dispatch is not configured.
	DispatchInputs []DispatchInput
}

// EventTrigger describes a single event subscription and its optional filters.
type EventTrigger struct {
	Event EventName
	// RepoPush filters (only meaningful for repo.push).
	Branches       []string
	BranchesIgnore []string
	Paths          []string
	PathsIgnore    []string
	// RepoPushTag filters (only meaningful for repo.push_tag).
	Tags       []string
	TagsIgnore []string
	// IssueComment filters (only meaningful for issue.comment).
	MentionedOnly bool
	FromRoles     []string
	FromUsers     []string
	// Dispatch inputs (only meaningful for workflow.dispatch).
	Inputs []DispatchInput
}

// EventName is a known workflow trigger event.
type EventName string

const (
	EventRepoPush         EventName = "repo.push"
	EventRepoPushTag      EventName = "repo.push_tag"
	EventIssueOpened      EventName = "issue.opened"
	EventIssueComment     EventName = "issue.comment"
	EventWorkflowDispatch EventName = "workflow.dispatch"
)

var validEventNames = map[EventName]bool{
	EventRepoPush:         true,
	EventRepoPushTag:      true,
	EventIssueOpened:      true,
	EventIssueComment:     true,
	EventWorkflowDispatch: true,
}

// DispatchInput declares a single input accepted by workflow.dispatch.
type DispatchInput struct {
	Name     string
	Required bool
}

// JobDefinition is a single job within a workflow.
type JobDefinition struct {
	Key              string
	DisplayName      string
	Env              map[string]string
	TimeoutMinutes   int
	WorkingDirectory string
	Steps            []StepDefinition
	Outputs          map[string]string
}

// Step type constants.
const (
	StepTypeRun     = "run"
	StepTypeRelease = "release"
)

// AssetDefinition is a single asset to attach to a release step.
type AssetDefinition struct {
	// Path is the container-relative or absolute path to the file.
	Path string
	// Name overrides the uploaded asset file name. When empty, the
	// basename of Path is used.
	Name string
}

// StepDefinition is a single step within a job. The Type field
// discriminates between run (shell) and release (built-in release
// creation) steps. When Type is empty, the step is treated as
// type "run" for backward compatibility.
type StepDefinition struct {
	Id      string
	Name    string
	Type    string            // "run" (default) or "release"
	Run     string            // shell command (only for type=run)
	Tag     string            // release tag name (only for type=release)
	Notes   string            // release notes (only for type=release)
	Draft   bool              // create as draft (only for type=release, default true)
	Assets  []AssetDefinition // assets to upload (only for type=release)
}

// WorkflowConfigValidationError collects all validation errors for a single
// workflow file so callers can surface them in one response.
type WorkflowConfigValidationError struct {
	SourceFile string
	Errors     []string
}

func (e *WorkflowConfigValidationError) Error() string {
	return fmt.Sprintf("workflow config %s validation: %v", e.SourceFile, e.Errors)
}

// WorkflowConfigSetValidationError collects errors across multiple workflow
// files in the same repo (e.g. duplicate workflow names).
type WorkflowConfigSetValidationError struct {
	Errors []string
}

func (e *WorkflowConfigSetValidationError) Error() string {
	return fmt.Sprintf("workflow config set validation: %v", e.Errors)
}
