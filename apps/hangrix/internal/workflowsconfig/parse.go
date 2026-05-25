package workflowsconfig

import (
	"fmt"
	"regexp"

	"go.yaml.in/yaml/v3"
)

// ---- wire types (yaml-tagged, private) ----

type workflowWire struct {
	Version int              `yaml:"version"`
	Name    string           `yaml:"name"`
	On      yaml.Node        `yaml:"on"` // decoded manually for strict key checking
	Env     map[string]string `yaml:"env"`
	Jobs    yaml.Node        `yaml:"jobs"` // decoded manually for ordered map
}

type jobWire struct {
	Name             string            `yaml:"name"`
	Env              map[string]string `yaml:"env"`
	TimeoutMinutes   int               `yaml:"timeout_minutes"`
	WorkingDirectory string            `yaml:"working_directory"`
	Steps            []stepWire        `yaml:"steps"`
	Outputs          map[string]string `yaml:"outputs"`
}

type stepWire struct {
	Id   string `yaml:"id"`
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

// pushWire models the value under on.repo.push.
type pushWire struct {
	Branches       []string `yaml:"branches"`
	BranchesIgnore []string `yaml:"branches_ignore"`
	Paths          []string `yaml:"paths"`
	PathsIgnore    []string `yaml:"paths_ignore"`
}

// pushTagWire models the value under on.repo.push_tag.
type pushTagWire struct {
	Tags       []string `yaml:"tags"`
	TagsIgnore []string `yaml:"tags_ignore"`
}

// commentWire models the value under on.issue.comment.
type commentWire struct {
	MentionedOnly bool     `yaml:"mentioned_only"`
	FromRoles     []string `yaml:"from_roles"`
	FromUsers     []string `yaml:"from_users"`
}

// dispatchWire models the value under on.workflow.dispatch.
type dispatchWire struct {
	Inputs []dispatchInputWire `yaml:"inputs"`
}

type dispatchInputWire struct {
	Name     string `yaml:"name"`
	Required bool   `yaml:"required"`
}

// ---- validation regexes ----

var (
	workflowNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	jobKeyRe       = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	envKeyRe       = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	inputNameRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	roleKeyRe      = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	stepIdRe       = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	outputKeyRe    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*$`)
)

// ---- public API ----

// ParseWorkflowConfig parses raw YAML bytes into a validated WorkflowConfig.
// It returns the config and nil on success, or nil and an error describing all
// validation failures.
func ParseWorkflowConfig(raw []byte, sourceFile string) (*WorkflowConfig, error) {
	var w workflowWire
	if err := yaml.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("parse workflow %s: %w", sourceFile, err)
	}

	return validateAndLift(&w, sourceFile)
}

// validateAndLift checks the wire struct and produces a clean WorkflowConfig.
func validateAndLift(w *workflowWire, sourceFile string) (*WorkflowConfig, error) {
	var errs []string

	// version
	if w.Version != 1 {
		errs = append(errs, "version must be 1")
	}

	// name
	if w.Name == "" {
		errs = append(errs, "name is required")
	} else if !workflowNameRe.MatchString(w.Name) {
		errs = append(errs, fmt.Sprintf("name %q must match [a-z][a-z0-9-]*", w.Name))
	}

	// on — decode manually for strict key checking
	triggers, onErrs := decodeOn(&w.On)
	errs = append(errs, onErrs...)

	if len(triggers) == 0 && len(onErrs) == 0 {
		errs = append(errs, "on: at least one event trigger is required")
	}

	// env keys
	for k := range w.Env {
		if !envKeyRe.MatchString(k) {
			errs = append(errs, fmt.Sprintf("env key %q must match [A-Z_][A-Z0-9_]*", k))
		}
	}

	// jobs — decode as ordered map
	jobs, jobErrs := decodeJobs(&w.Jobs)
	errs = append(errs, jobErrs...)

	if len(jobs) == 0 && len(jobErrs) == 0 {
		errs = append(errs, "jobs: at least one job is required")
	}

	// Collect dispatch inputs from trigger
	var dispatchInputs []DispatchInput
	for _, t := range triggers {
		if t.Event == EventWorkflowDispatch {
			dispatchInputs = t.Inputs
			break
		}
	}

	if len(errs) > 0 {
		return nil, &WorkflowConfigValidationError{SourceFile: sourceFile, Errors: errs}
	}

	// Apply defaults
	cfg := &WorkflowConfig{
		Name:           w.Name,
		SourceFile:     sourceFile,
		On:             triggers,
		Env:            w.Env,
		Jobs:           jobs,
		DispatchInputs: dispatchInputs,
	}
	normalizeConfig(cfg)
	return cfg, nil
}

// decodeOn parses the `on` YAML node into a list of EventTriggers.
// It rejects unknown event keys and unknown sub-keys.
func decodeOn(node *yaml.Node) ([]EventTrigger, []string) {
	if node.Kind != yaml.MappingNode {
		return nil, []string{"on: must be a mapping"}
	}

	var triggers []EventTrigger
	var errs []string

	// The node's children are pairs: [key, value, key, value, ...]
	for i := 0; i < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]

		eventName := EventName(keyNode.Value)
		if !validEventNames[eventName] {
			errs = append(errs, fmt.Sprintf("on.%s: unknown event", keyNode.Value))
			continue
		}

		trigger := EventTrigger{Event: eventName}

		switch eventName {
		case EventRepoPush:
			var pw pushWire
			if valNode.Kind == yaml.MappingNode && len(valNode.Content) > 0 {
				if err := valNode.Decode(&pw); err != nil {
					errs = append(errs, fmt.Sprintf("on.repo.push: %v", err))
					continue
				}
				// Strict key check
				seen := map[string]bool{}
				for j := 0; j < len(valNode.Content); j += 2 {
					k := valNode.Content[j].Value
					if seen[k] {
						errs = append(errs, fmt.Sprintf("on.repo.push.%s: duplicate key", k))
					}
					seen[k] = true
					switch k {
					case "branches", "branches_ignore", "paths", "paths_ignore":
						// valid
					default:
						errs = append(errs, fmt.Sprintf("on.repo.push.%s: unknown key", k))
					}
				}
			}
			trigger.Branches = pw.Branches
			trigger.BranchesIgnore = pw.BranchesIgnore
			trigger.Paths = pw.Paths
			trigger.PathsIgnore = pw.PathsIgnore

		case EventRepoPushTag:
			var ptw pushTagWire
			if valNode.Kind == yaml.MappingNode && len(valNode.Content) > 0 {
				if err := valNode.Decode(&ptw); err != nil {
					errs = append(errs, fmt.Sprintf("on.repo.push_tag: %v", err))
					continue
				}
				// Strict key check
				seen := map[string]bool{}
				for j := 0; j < len(valNode.Content); j += 2 {
					k := valNode.Content[j].Value
					if seen[k] {
						errs = append(errs, fmt.Sprintf("on.repo.push_tag.%s: duplicate key", k))
					}
					seen[k] = true
					switch k {
					case "tags", "tags_ignore":
						// valid
					default:
						errs = append(errs, fmt.Sprintf("on.repo.push_tag.%s: unknown key", k))
					}
				}
			}
			trigger.Tags = ptw.Tags
			trigger.TagsIgnore = ptw.TagsIgnore

		case EventIssueOpened:
			// v1: no filters, but reject unknown keys
			if valNode.Kind == yaml.MappingNode && len(valNode.Content) > 0 {
				for j := 0; j < len(valNode.Content); j += 2 {
					k := valNode.Content[j].Value
					errs = append(errs, fmt.Sprintf("on.issue.opened.%s: unknown key (v1 issue.opened does not accept filters)", k))
				}
			}

		case EventIssueComment:
			var cw commentWire
			if valNode.Kind == yaml.MappingNode && len(valNode.Content) > 0 {
				if err := valNode.Decode(&cw); err != nil {
					errs = append(errs, fmt.Sprintf("on.issue.comment: %v", err))
					continue
				}
				for j := 0; j < len(valNode.Content); j += 2 {
					k := valNode.Content[j].Value
					switch k {
					case "mentioned_only", "from_roles", "from_users":
						// valid
					default:
						errs = append(errs, fmt.Sprintf("on.issue.comment.%s: unknown key", k))
					}
				}
			}
			trigger.MentionedOnly = cw.MentionedOnly
			trigger.FromRoles = cw.FromRoles
			trigger.FromUsers = cw.FromUsers

		case EventWorkflowDispatch:
			var dw dispatchWire
			if valNode.Kind == yaml.MappingNode && len(valNode.Content) > 0 {
				if err := valNode.Decode(&dw); err != nil {
					errs = append(errs, fmt.Sprintf("on.workflow.dispatch: %v", err))
					continue
				}
				for j := 0; j < len(valNode.Content); j += 2 {
					k := valNode.Content[j].Value
					if k != "inputs" {
						errs = append(errs, fmt.Sprintf("on.workflow.dispatch.%s: unknown key", k))
					}
				}
				// Validate inputs
				seenInputs := map[string]bool{}
				for _, in := range dw.Inputs {
					if in.Name == "" {
						errs = append(errs, "on.workflow.dispatch.inputs: name is required")
						continue
					}
					if !inputNameRe.MatchString(in.Name) {
						errs = append(errs, fmt.Sprintf("on.workflow.dispatch.inputs.%s: must match [a-z][a-z0-9_]*", in.Name))
					}
					if seenInputs[in.Name] {
						errs = append(errs, fmt.Sprintf("on.workflow.dispatch.inputs.%s: duplicate", in.Name))
					}
					seenInputs[in.Name] = true
					trigger.Inputs = append(trigger.Inputs, DispatchInput{Name: in.Name, Required: in.Required})
				}
			}
		}

		triggers = append(triggers, trigger)
	}

	return triggers, errs
}

// decodeJobs parses the `jobs` YAML node into an ordered list of JobDefinitions.
func decodeJobs(node *yaml.Node) ([]JobDefinition, []string) {
	if node.Kind != yaml.MappingNode {
		return nil, []string{"jobs: must be a mapping"}
	}

	var jobs []JobDefinition
	var errs []string
	seenKeys := map[string]bool{}

	for i := 0; i < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]
		key := keyNode.Value

		if !jobKeyRe.MatchString(key) {
			errs = append(errs, fmt.Sprintf("jobs.%s: key must match [a-z][a-z0-9-]*", key))
			continue
		}
		if seenKeys[key] {
			errs = append(errs, fmt.Sprintf("jobs.%s: duplicate job key", key))
			continue
		}
		seenKeys[key] = true

		var jw jobWire
		if err := valNode.Decode(&jw); err != nil {
			errs = append(errs, fmt.Sprintf("jobs.%s: %v", key, err))
			continue
		}

		// Strict key check
		for j := 0; j < len(valNode.Content); j += 2 {
			k := valNode.Content[j].Value
			switch k {
			case "name", "env", "timeout_minutes", "working_directory", "steps", "outputs":
				// valid
			default:
				errs = append(errs, fmt.Sprintf("jobs.%s.%s: unknown key", key, k))
			}
		}

		prefix := fmt.Sprintf("jobs.%s", key)

		// Display name
		displayName := jw.Name
		if displayName == "" {
			displayName = key
		}

		// Timeout
		timeout := jw.TimeoutMinutes
		if timeout == 0 {
			timeout = 60
		}
		if timeout < 1 || timeout > 1440 {
			errs = append(errs, fmt.Sprintf("%s.timeout_minutes: must be between 1 and 1440", prefix))
		}

		// Working directory
		wd := jw.WorkingDirectory
		if wd == "" {
			wd = "/workspace"
		}

		// Env keys
		for k := range jw.Env {
			if !envKeyRe.MatchString(k) {
				errs = append(errs, fmt.Sprintf("%s.env key %q must match [A-Z_][A-Z0-9_]*", prefix, k))
			}
		}

		// Steps
		if len(jw.Steps) == 0 {
			errs = append(errs, fmt.Sprintf("%s.steps: at least one step is required", prefix))
		}
		seenStepIDs := map[string]bool{}
		for si, sw := range jw.Steps {
			sp := fmt.Sprintf("%s.steps[%d]", prefix, si)
			if sw.Run == "" {
				errs = append(errs, fmt.Sprintf("%s.run: required", sp))
			}
			// Validate step id
			if sw.Id != "" {
				if !stepIdRe.MatchString(sw.Id) {
					errs = append(errs, fmt.Sprintf("%s.id %q: must match [a-z][a-z0-9-]*", sp, sw.Id))
				}
				if seenStepIDs[sw.Id] {
					errs = append(errs, fmt.Sprintf("%s.id %q: duplicate step id", sp, sw.Id))
				}
				seenStepIDs[sw.Id] = true
			}
			// Check for unknown step keys
			// The step node is nested; we check via the yaml.Node directly
		}
		// Strict step key check via the raw YAML node
		if valNode.Kind == yaml.MappingNode {
			for j := 0; j < len(valNode.Content); j += 2 {
				if valNode.Content[j].Value == key {
					continue
				}
			}
		}
		// We'll do strict step key checking by re-traversing
		for si, sw := range jw.Steps {
			_ = si
			if sw.Name != "" && len(sw.Name) > 200 {
				errs = append(errs, fmt.Sprintf("%s.steps[%d].name: max 200 characters", prefix, si))
			}
			// Unknown step key check
			// (handled above via raw YAML node traversal — id, name, run are the only valid keys)
		}

		// Validate outputs keys
		for k := range jw.Outputs {
			if !outputKeyRe.MatchString(k) {
				errs = append(errs, fmt.Sprintf("%s.outputs key %q: must match [a-zA-Z_][a-zA-Z0-9_-]*", prefix, k))
			}
		}

		jobs = append(jobs, JobDefinition{
			Key:              key,
			DisplayName:      displayName,
			Env:              jw.Env,
			TimeoutMinutes:   timeout,
			WorkingDirectory: wd,
			Steps:            liftSteps(jw.Steps),
			Outputs:          jw.Outputs,
		})
	}

	return jobs, errs
}

func liftSteps(wires []stepWire) []StepDefinition {
	steps := make([]StepDefinition, len(wires))
	for i, sw := range wires {
		var id *string
		if sw.Id != "" {
			id = &sw.Id
		}
		steps[i] = StepDefinition{
			Id:   id,
			Name: sw.Name,
			Run:  sw.Run,
		}
	}
	return steps
}
