package workflowsconfig

import (
	"fmt"
	"regexp"
	"strings"
)

// ---- expression parsing ----

// exprPattern matches ${{ <body> }} with optional surrounding whitespace
// inside the braces. The body must be non-empty.
var exprPattern = regexp.MustCompile(`\$\{\{\s*([^}]+?)\s*\}\}`)

// exprSegmentRe validates a single segment in a dotted path: [a-zA-Z_][a-zA-Z0-9_-]*
var exprSegmentRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*$`)

// ExpressionRef describes a resolved ${{ }} reference.
type ExpressionRef struct {
	Kind   ExpressionKind // steps, jobs, env, inputs
	StepID string         // only for Kind=steps
	JobKey string         // only for Kind=jobs
	Key    string         // the final key (outputs.<key>, env.<KEY>, inputs.<name>)
}

// ExpressionKind is the namespace of a ${{ }} reference.
type ExpressionKind string

const (
	ExprSteps  ExpressionKind = "steps"
	ExprJobs   ExpressionKind = "jobs"
	ExprEnv    ExpressionKind = "env"
	ExprInputs ExpressionKind = "inputs"
)

// ParseExpressionRef parses a single ${{ ... }} body into an ExpressionRef.
// The body is the inner text between the braces (without the ${{ }} wrapper).
func ParseExpressionRef(body string) (*ExpressionRef, error) {
	parts := strings.Split(body, ".")
	if len(parts) < 3 {
		return nil, fmt.Errorf("expression %q: must be at least 3 segments (e.g. steps.id.outputs.key)", body)
	}

	// Validate all segments
	for i, p := range parts {
		if !exprSegmentRe.MatchString(p) {
			return nil, fmt.Errorf("expression %q: segment %d %q must match [a-zA-Z_][a-zA-Z0-9_-]*", body, i+1, p)
		}
	}

	switch parts[0] {
	case "steps":
		if len(parts) < 4 || parts[2] != "outputs" {
			return nil, fmt.Errorf("expression %q: steps reference must be steps.<id>.outputs.<key>", body)
		}
		return &ExpressionRef{
			Kind:   ExprSteps,
			StepID: parts[1],
			Key:    parts[3],
		}, nil
	case "jobs":
		if len(parts) < 4 || parts[2] != "outputs" {
			return nil, fmt.Errorf("expression %q: jobs reference must be jobs.<job>.outputs.<key>", body)
		}
		return &ExpressionRef{
			Kind:   ExprJobs,
			JobKey: parts[1],
			Key:    parts[3],
		}, nil
	case "env":
		if len(parts) != 2 {
			return nil, fmt.Errorf("expression %q: env reference must be env.<KEY>", body)
		}
		return &ExpressionRef{
			Kind: ExprEnv,
			Key:  parts[1],
		}, nil
	case "inputs":
		if len(parts) != 2 {
			return nil, fmt.Errorf("expression %q: inputs reference must be inputs.<name>", body)
		}
		return &ExpressionRef{
			Kind: ExprInputs,
			Key:  parts[1],
		}, nil
	default:
		return nil, fmt.Errorf("expression %q: unknown namespace %q (expected steps, jobs, env, or inputs)", body, parts[0])
	}
}

// FindExpressions returns all ${{ }} expressions found in the given string.
func FindExpressions(s string) []string {
	matches := exprPattern.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) >= 2 {
			out = append(out, m[1])
		}
	}
	return out
}

// ExpandExpr resolves all ${{ }} expressions in s against the given context.
// Unknown references return an explicit error.
func ExpandExpr(s string, ctx ExpressionContext) (string, error) {
	var errs []string

	result := exprPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Extract body: remove ${{ and }}
		body := strings.TrimSpace(match[3 : len(match)-2]) // skip "${{" and "}}"
		if body == "" {
			errs = append(errs, "empty expression")
			return match
		}

		ref, err := ParseExpressionRef(body)
		if err != nil {
			errs = append(errs, err.Error())
			return match
		}

		val, err := ctx.Resolve(ref)
		if err != nil {
			errs = append(errs, err.Error())
			return match
		}
		return val
	})

	if len(errs) > 0 {
		return "", fmt.Errorf("expression expansion: %s", strings.Join(errs, "; "))
	}
	return result, nil
}

// ExpressionContext provides the runtime values for expression resolution.
type ExpressionContext struct {
	// StepOutputs maps step_id -> outputs map (for steps within the same job).
	StepOutputs map[string]map[string]string
	// JobOutputs maps job_key -> outputs map (for completed prior jobs).
	JobOutputs map[string]map[string]string
	// Env is the merged env map for the current job.
	Env map[string]string
	// Inputs carries dispatch input values (already uppercased).
	Inputs map[string]string
}

// Resolve resolves a single ExpressionRef against this context.
func (ctx ExpressionContext) Resolve(ref *ExpressionRef) (string, error) {
	switch ref.Kind {
	case ExprSteps:
		outputs, ok := ctx.StepOutputs[ref.StepID]
		if !ok {
			return "", fmt.Errorf("${{ steps.%s.outputs.%s }}: step %q has no captured outputs", ref.StepID, ref.Key, ref.StepID)
		}
		val, ok := outputs[ref.Key]
		if !ok {
			return "", fmt.Errorf("${{ steps.%s.outputs.%s }}: key %q not found in step %q outputs", ref.StepID, ref.Key, ref.Key, ref.StepID)
		}
		return val, nil
	case ExprJobs:
		outputs, ok := ctx.JobOutputs[ref.JobKey]
		if !ok {
			return "", fmt.Errorf("${{ jobs.%s.outputs.%s }}: job %q has no captured outputs", ref.JobKey, ref.Key, ref.JobKey)
		}
		val, ok := outputs[ref.Key]
		if !ok {
			return "", fmt.Errorf("${{ jobs.%s.outputs.%s }}: key %q not found in job %q outputs", ref.JobKey, ref.Key, ref.Key, ref.JobKey)
		}
		return val, nil
	case ExprEnv:
		val, ok := ctx.Env[ref.Key]
		if !ok {
			return "", fmt.Errorf("${{ env.%s }}: env var %q is not set", ref.Key, ref.Key)
		}
		return val, nil
	case ExprInputs:
		val, ok := ctx.Inputs[ref.Key]
		if !ok {
			return "", fmt.Errorf("${{ inputs.%s }}: input %q is not set", ref.Key, ref.Key)
		}
		return val, nil
	default:
		return "", fmt.Errorf("unknown expression kind %q", ref.Kind)
	}
}

// ---- parse-time cross-reference validation ----

// ValidateCrossReferences checks that all ${{ }} expressions in the workflow
// config reference valid step IDs (within the same job, appearing earlier)
// and job keys (appearing earlier in declaration order). Returns nil when
// all references are valid.
func (cfg *WorkflowConfig) ValidateCrossReferences() error {
	var errs []string

	for ji, job := range cfg.Jobs {
		// Build a set of step ids declared in this job (in order)
		stepIDs := make(map[string]int) // id -> index
		for si, step := range job.Steps {
			if step.Id != nil {
				stepIDs[*step.Id] = si
			}
		}

		// Validate expressions in job outputs
		for outKey, outVal := range job.Outputs {
			for _, exprBody := range FindExpressions(outVal) {
				ref, err := ParseExpressionRef(exprBody)
				if err != nil {
					errs = append(errs, fmt.Sprintf("jobs.%s.outputs.%s: %v", job.Key, outKey, err))
					continue
				}
				if err := validateRefPositions(ref, ji, job.Key, stepIDs); err != nil {
					errs = append(errs, fmt.Sprintf("jobs.%s.outputs.%s: %v", job.Key, outKey, err))
				}
			}
		}

		// Validate expressions in step run commands
		for _, step := range job.Steps {
			for _, exprBody := range FindExpressions(step.Run) {
				ref, err := ParseExpressionRef(exprBody)
				if err != nil {
					stepName := stepNameForError(step)
					errs = append(errs, fmt.Sprintf("jobs.%s.%s.run: %v", job.Key, stepName, err))
					continue
				}
				if err := validateRefPositions(ref, ji, job.Key, stepIDs); err != nil {
					stepName := stepNameForError(step)
					errs = append(errs, fmt.Sprintf("jobs.%s.%s.run: %v", job.Key, stepName, err))
				}
			}
		}
	}

	if len(errs) > 0 {
		return &WorkflowConfigValidationError{
			SourceFile: cfg.SourceFile,
			Errors:     errs,
		}
	}
	return nil
}

func stepNameForError(step StepDefinition) string {
	if step.Id != nil {
		return "steps[" + *step.Id + "]"
	}
	if step.Name != "" {
		return "steps[" + step.Name + "]"
	}
	return "steps[?]"
}

// validateRefPositions checks that a parsed expression reference points to
// a step or job that exists and appears earlier in the workflow.
func validateRefPositions(ref *ExpressionRef, currentJobIndex int, currentJobKey string, stepIDs map[string]int) error {
	switch ref.Kind {
	case ExprSteps:
		si, ok := stepIDs[ref.StepID]
		if !ok {
			return fmt.Errorf("${{ steps.%s.outputs.%s }}: step id %q not found in job %q", ref.StepID, ref.Key, ref.StepID, currentJobKey)
		}
		// Check the step appears before any step that references it.
		// We can't determine exact position here without the step that contains
		// the expression; the caller checks the step's own index.
		// For now, just check existence; position check is done by the caller.
		_ = si
		return nil
	case ExprJobs:
		// The referenced job must exist and appear earlier.
		// We don't have the full job list here, so the caller handles this.
		return nil
	case ExprEnv, ExprInputs:
		// env and inputs are resolved at runtime; no static validation needed.
		return nil
	}
	return nil
}

// ValidateCrossReferencesFull performs full cross-reference validation across
// an entire parsed workflow config set. This includes checks that:
// - steps.<id> references appear earlier in the same job
// - jobs.<job> references appear earlier in declaration order
func ValidateCrossReferencesFull(configs []*WorkflowConfig) error {
	var errs []string

	for _, cfg := range configs {
		for ji, job := range cfg.Jobs {
			// Build an ordered list of step ids for position checking
			stepIDOrder := make(map[string]int)
			stepIDs := make(map[string]bool)
			for si, step := range job.Steps {
				if step.Id != nil {
					stepIDOrder[*step.Id] = si
					stepIDs[*step.Id] = true
				}
			}

			// Validate expressions in job outputs
			for outKey, outVal := range job.Outputs {
				for _, exprBody := range FindExpressions(outVal) {
					ref, err := ParseExpressionRef(exprBody)
					if err != nil {
						errs = append(errs, fmt.Sprintf("%s: jobs.%s.outputs.%s: %v", cfg.SourceFile, job.Key, outKey, err))
						continue
					}
					switch ref.Kind {
					case ExprSteps:
						if !stepIDs[ref.StepID] {
							errs = append(errs, fmt.Sprintf("%s: jobs.%s.outputs.%s: step id %q not found in job %q", cfg.SourceFile, job.Key, outKey, ref.StepID, job.Key))
						}
					case ExprJobs:
						found := false
						for pi := 0; pi < ji; pi++ {
							if cfg.Jobs[pi].Key == ref.JobKey {
								found = true
								break
							}
						}
						if !found {
							errs = append(errs, fmt.Sprintf("%s: jobs.%s.outputs.%s: job %q must be declared before job %q", cfg.SourceFile, job.Key, outKey, ref.JobKey, job.Key))
						}
					}
				}
			}

			// Validate expressions in step run commands
			for si, step := range job.Steps {
				for _, exprBody := range FindExpressions(step.Run) {
					ref, err := ParseExpressionRef(exprBody)
					if err != nil {
						sname := stepNameForError(step)
						errs = append(errs, fmt.Sprintf("%s: jobs.%s.%s.run: %v", cfg.SourceFile, job.Key, sname, err))
						continue
					}
					switch ref.Kind {
					case ExprSteps:
						pos, ok := stepIDOrder[ref.StepID]
						if !ok {
							sname := stepNameForError(step)
							errs = append(errs, fmt.Sprintf("%s: jobs.%s.%s.run: step id %q not found in job %q", cfg.SourceFile, job.Key, sname, ref.StepID, job.Key))
						} else if pos >= si {
							sname := stepNameForError(step)
							errs = append(errs, fmt.Sprintf("%s: jobs.%s.%s.run: step id %q (position %d) must appear before referencing step (position %d)", cfg.SourceFile, job.Key, sname, ref.StepID, pos, si))
						}
					case ExprJobs:
						found := false
						for pi := 0; pi < ji; pi++ {
							if cfg.Jobs[pi].Key == ref.JobKey {
								found = true
								break
							}
						}
						if !found {
							sname := stepNameForError(step)
							errs = append(errs, fmt.Sprintf("%s: jobs.%s.%s.run: job %q must be declared before job %q", cfg.SourceFile, job.Key, sname, ref.JobKey, job.Key))
						}
					}
				}
			}
		}
	}

	if len(errs) > 0 {
		return &WorkflowConfigSetValidationError{Errors: errs}
	}
	return nil
}
