package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// compactSessionTool exposes the schema for the `compact_session` memory
// operation the LLM uses to release context space. The tool itself is a
// schema-only stub: the runtime loop intercepts the call by name and
// rewrites the in-memory context so that the next LLM turn starts from
// the summary the model just wrote. We register it as a normal Tool so
// the descriptor flows through the existing catalogue/allow-list path
// without a parallel registration channel.
//
// Why schema-only: the side effect (anchor the conversation window on a
// fresh summary) is a mutation of the loop's Context, not an external
// I/O action. Keeping Call() side-effect-free preserves the Tool
// contract — the loop reads the same args and does the context surgery
// where it belongs.
type compactSessionTool struct{}

// NewCompactSessionTool returns the schema-only tool. Exported so
// tools/module.go can wire it next to the other locals.
func NewCompactSessionTool() Tool { return compactSessionTool{} }

// CompactSessionToolName is the canonical name. Exported so the runtime
// loop can match on it without a string-literal duplicate.
const CompactSessionToolName = "compact_session"

func (compactSessionTool) Name() string { return CompactSessionToolName }

func (compactSessionTool) Description() string {
	return strings.Join([]string{
		"Compact the current session's conversation memory into a single summary, then continue from that summary. Call this when the current task is finished and the next event you're about to handle is unrelated, OR when the conversation has accumulated a lot of stale tool output and you want to free context space before the next step.",
		"The `summary` you write is the ONLY memory the next turn will have of everything before this call. It MUST capture: (1) decisions already taken and why, (2) outstanding work and what the next role/step should do, (3) key facts the next turn cannot re-derive — file paths edited, branch state, commit shas, issue / PR numbers, identifiers passed in tool results, blockers raised. Write it as compact prose, not a chat transcript replay; future-you is the reader.",
		"Effects: prior messages remain on the audit record but are no longer sent to the LLM on subsequent calls — your view rewinds to {system prompt} + {this summary} + anything new that arrives after. There is no rollback; once compacted, the detail is gone from your working memory.",
		"Call this tool ALONE in its tool-call batch — do not pair it with other tool calls in the same response. Mixing breaks the round's bookkeeping and the sibling tool's result will not be visible after the compact.",
	}, " ")
}

func (compactSessionTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{
				"type":        "string",
				"description": "The summary that replaces the prior conversation. Cover completed decisions (with rationale), outstanding work, and any key facts the next turn must carry forward (file paths, branch state, commit shas, issue/PR numbers, identifiers seen in earlier tool results). Compact prose; not a transcript replay.",
				"minLength":   1,
			},
		},
		"required": []string{"summary"},
	}
}

// Call is unreachable in production — the runtime loop intercepts
// compact_session before dispatch and applies the summary directly to
// the Context. It's wired so that if the loop ever stops intercepting
// (e.g. a future refactor), the tool fails loudly instead of silently
// no-ops and the summary getting lost.
func (compactSessionTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	return nil, errors.New("compact_session: runtime did not intercept this call. This is an agent bug — the compact_session tool's effect must be applied by the runtime loop, not by tool dispatch. Report this so the loop's intercept path can be fixed.")
}

// CompactSessionArgs is the parsed shape of the LLM's arguments to
// compact_session. Exposed so the runtime loop can decode the same
// envelope the schema documents without re-declaring the struct.
type CompactSessionArgs struct {
	Summary string `json:"summary"`
}

// ParseCompactSessionArgs decodes and validates one compact_session
// invocation. Returns the trimmed summary or an error suitable for
// feeding back to the LLM as the tool result.
func ParseCompactSessionArgs(raw json.RawMessage) (string, error) {
	var a CompactSessionArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	s := strings.TrimSpace(a.Summary)
	if s == "" {
		return "", fmt.Errorf("compact_session: 'summary' is empty. The summary is the only memory the next turn carries forward — write a compact prose recap covering completed decisions, outstanding work, and key facts (paths, branches, commit shas, IDs). Do not call compact_session with an empty summary.")
	}
	return s, nil
}
