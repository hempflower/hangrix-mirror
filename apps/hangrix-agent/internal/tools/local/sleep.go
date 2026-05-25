package local

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// sleepTool schedules an asynchronous pause: it returns immediately with a
// "scheduled" status, and the runtime delivers a notification to the LLM
// context when the timer expires. It replaces the old synchronous sleep
// (which blocked the tool-call round-trip) with the same async-notification
// model that background bash tasks use.
//
// The tool itself is deliberately minimal — integer seconds with a hard
// upper bound of 300 — so the LLM can't self-inflict a hang. The runtime
// cancels any pending sleep on session shutdown via the AsyncLifecycle
// Cleanup path.
type sleepTool struct {
	async AsyncLifecycle
}

func newSleepTool(async AsyncLifecycle) Tool {
	return &sleepTool{async: async}
}

const SleepToolName = "sleep"

func (sleepTool) Name() string { return SleepToolName }

func (sleepTool) Description() string {
	return strings.Join([]string{
		"Schedule an asynchronous pause for the specified number of seconds.",
		"This is an ASYNC tool: it returns immediately with status \"scheduled\" — returning does NOT mean the wait is over.",
		"The runtime will post a completion notification to your context when the timer expires; you will be woken up to continue.",
		"After calling sleep, if you have no other parallel work, end the current turn so the agent enters idle and waits for the wake-up notification.",
		"Do NOT use sleep as a synchronous delay or as a throttle between tool calls.",
		"Only integer seconds; maximum 300 (5 minutes).",
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

func (t *sleepTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
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

	// Generate the ID first so it can be embedded in the completion
	// notification. This lets the LLM distinguish concurrent sleeps
	// (with or without reason) when the notification fires.
	sleepID := newSleepID()

	// Build the completion notification that the runtime will inject into
	// the LLM context when this timer fires. Include the sleep_id so the
	// model knows exactly which sleep finished, plus seconds and reason
	// so it has enough context to continue without polling.
	reasonAttr := ""
	if a.Reason != "" {
		reasonAttr = fmt.Sprintf(" reason=\"%s\"", xmlEscapeAttr(a.Reason))
	}
	notification := fmt.Sprintf(
		"<hangrix-event kind=\"notification.sleep.finished\" id=\"%s\" status=\"done\">"+
			"<sleep seconds=\"%d\"%s/>"+
			"</hangrix-event>",
		sleepID, a.Seconds, reasonAttr,
	)

	t.async.ScheduleWithID(sleepID, d, notification)

	return map[string]any{
		"status":   "scheduled",
		"sleep_id": sleepID,
		"seconds":  a.Seconds,
		"reason":   a.Reason,
		"note":     "Sleep has been scheduled but is NOT yet complete. The runtime will wake you with a notification when the timer expires. If you have no other parallel work, end the current turn now to wait for the wake-up.",
	}, nil
}
