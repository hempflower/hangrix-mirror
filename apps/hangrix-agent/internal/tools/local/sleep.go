package local

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// sleepTool pauses the agent for a caller-specified number of seconds.
// It exists so the LLM can wait for background bash tasks (or other
// eventual side effects) to land without burning round-trips on empty
// polls. The design is deliberately minimal: integer seconds with a
// hard upper bound of 300 (5 min) so the agent can't self-inflict a
// hang that outlasts a session watchdog.
//
// Context cancellation (control:shutdown, runner closes the pipe) is
// honoured immediately — the tool selects on ctx.Done() alongside the
// timer so the agent exits promptly when told to.
type sleepTool struct{}

func newSleepTool() Tool { return sleepTool{} }

const SleepToolName = "sleep"

func (sleepTool) Name() string { return SleepToolName }

func (sleepTool) Description() string {
	return strings.Join([]string{
		"Pause execution for the specified number of seconds before returning. Use this to wait for a background task (started with bash run_in_background=true) to complete before polling it, rather than spending a round-trip on an empty poll. Only integer seconds; maximum 300 (5 minutes). The call returns early if the session is cancelled.",
	}, " ")
}

func (sleepTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"seconds": map[string]any{
				"type":        "integer",
				"description": "Number of seconds to wait. Must be between 1 and 300 inclusive.",
				"minimum":     1,
				"maximum":     300,
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Optional explanation for why the wait is needed (e.g. 'waiting for npm install to finish').",
			},
		},
		"required": []string{"seconds"},
	}
}

type sleepArgs struct {
	Seconds int    `json:"seconds"`
	Reason  string `json:"reason"`
}

func (sleepTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	var a sleepArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.Seconds < 1 {
		return nil, fmt.Errorf("sleep: seconds must be at least 1 (got %d). Sleep is for waiting on background tasks; for shorter pauses use a smaller value, and for immediate re-check use bash(task_id=...) to poll without sleeping.", a.Seconds)
	}
	if a.Seconds > 300 {
		return nil, fmt.Errorf("sleep: maximum 300 seconds (got %d). Use a shorter wait and re-check, or poll the background task with bash(task_id=...).", a.Seconds)
	}

	d := time.Duration(a.Seconds) * time.Second
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("sleep: interrupted: %w", ctx.Err())
	case <-timer.C:
	}

	return map[string]any{
		"seconds": a.Seconds,
		"reason":  a.Reason,
	}, nil
}
