package workflowsconfig

import (
	"fmt"
	"regexp"

	"go.yaml.in/yaml/v3"
)

// ---- wire types (yaml-tagged, private) ----

type workflowWire struct {
	Version int               `yaml:"version"`
	Name    string            `yaml:"name"`
	On      yaml.Node         `yaml:"on"` // decoded manually for strict key checking
	Env     map[string]string `yaml:"env"`
	Jobs    yaml.Node         `yaml:"jobs"` // decoded manually for ordered map
}

type jobWire struct {
	Name           string            `yaml:"name"`
	Env            map[string]string `yaml:"env"`
	TimeoutMinutes int               `yaml:"timeout_minutes"`
	Dir            string            `yaml:"dir"`
	Steps          []stepWire        `yaml:"steps"`
	Outputs        map[string]string `yaml:"outputs"`
}

type stepWire struct {
	Id   string `yaml:"id"`
	Name string `yaml:"name"`
	Type string `yaml:"type"` // "" / "run" = shell step; otherwise a built-in type
	// Shell-step fields (type run / omitted). Env is merged over the
	// job/container env; Dir overrides the job working directory for
	// this step only.
	Run string            `yaml:"run"`
	Env map[string]string `yaml:"env"`
	Dir string            `yaml:"dir"`
	// With carries parameters for built-in typed steps (e.g. release),
	// mirroring GitHub Actions' `with:`. Interpreted per step type, so
	// the shared step schema never grows when a new type is added.
	With map[string]any `yaml:"with"`
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
				// Lenient: unknown sub-keys are ignored, not rejected.
				if err := valNode.Decode(&pw); err != nil {
					errs = append(errs, fmt.Sprintf("on.repo.push: %v", err))
					continue
				}
			}
			trigger.Branches = pw.Branches
			trigger.BranchesIgnore = pw.BranchesIgnore
			trigger.Paths = pw.Paths
			trigger.PathsIgnore = pw.PathsIgnore

		case EventRepoPushTag:
			var ptw pushTagWire
			if valNode.Kind == yaml.MappingNode && len(valNode.Content) > 0 {
				// Lenient: unknown sub-keys are ignored, not rejected.
				if err := valNode.Decode(&ptw); err != nil {
					errs = append(errs, fmt.Sprintf("on.repo.push_tag: %v", err))
					continue
				}
			}
			trigger.Tags = ptw.Tags
			trigger.TagsIgnore = ptw.TagsIgnore

		case EventIssueOpened:
			// v1 takes no filters; any provided keys are ignored (lenient).

		case EventIssueComment:
			var cw commentWire
			if valNode.Kind == yaml.MappingNode && len(valNode.Content) > 0 {
				// Lenient: unknown sub-keys are ignored, not rejected.
				if err := valNode.Decode(&cw); err != nil {
					errs = append(errs, fmt.Sprintf("on.issue.comment: %v", err))
					continue
				}
			}
			trigger.MentionedOnly = cw.MentionedOnly
			trigger.FromRoles = cw.FromRoles
			trigger.FromUsers = cw.FromUsers

		case EventWorkflowDispatch:
			var dw dispatchWire
			if valNode.Kind == yaml.MappingNode && len(valNode.Content) > 0 {
				// Lenient: unknown sub-keys are ignored, not rejected.
				if err := valNode.Decode(&dw); err != nil {
					errs = append(errs, fmt.Sprintf("on.workflow.dispatch: %v", err))
					continue
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

		// Lenient: unknown job keys are ignored, not rejected.

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
		wd := jw.Dir
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

		// Per-step validation. Lenient: unknown keys are ignored rather
		// than rejected. Only structural requirements are enforced — the
		// required field per type and the step name length. Type-specific
		// params for built-in steps live under `with:` and are interpreted
		// by the runner, so adding a step type never touches this loop.
		for si, sw := range jw.Steps {
			sp := fmt.Sprintf("%s.steps[%d]", prefix, si)

			stepType := sw.Type
			if stepType == "" {
				stepType = StepTypeRun
			}
			switch stepType {
			case StepTypeRun:
				if sw.Run == "" {
					errs = append(errs, fmt.Sprintf("%s.run: required for type run", sp))
				}
			case StepTypeRelease:
				if asString(sw.With["tag"]) == "" {
					errs = append(errs, fmt.Sprintf("%s.with.tag: required for type release", sp))
				}
			default:
				errs = append(errs, fmt.Sprintf("%s.type: unknown step type %q (must be run or release)", sp, stepType))
			}

			if sw.Name != "" && len(sw.Name) > 200 {
				errs = append(errs, fmt.Sprintf("%s.name: max 200 characters", sp))
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

// asString returns the string value of a YAML-decoded `with` entry, or ""
// for nil / non-string values. Used for lenient `with` field reads.
func asString(v any) string {
	s, _ := v.(string)
	return s
}

func liftSteps(wires []stepWire) []StepDefinition {
	steps := make([]StepDefinition, len(wires))
	for i, sw := range wires {
		// Default type.
		stepType := sw.Type
		if stepType == "" {
			stepType = StepTypeRun
		}

		steps[i] = StepDefinition{
			Id:   sw.Id,
			Name: sw.Name,
			Type: stepType,
			Run:  sw.Run,
			Env:  sw.Env,
			Dir:  sw.Dir,
			With: sw.With,
		}
	}
	return steps
}
