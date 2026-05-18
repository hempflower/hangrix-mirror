package runtime_test

import (
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/runtime"
)

// TestSnapshotNoSummaryReturnsFullHistory verifies the no-summary base
// case — the LLM-facing window covers every message ever appended,
// since there is no prior compact point to anchor on.
func TestSnapshotNoSummaryReturnsFullHistory(t *testing.T) {
	t.Parallel()

	cctx := runtime.NewContext("sys", nil)
	cctx.AppendUser("first")
	cctx.AppendAssistant("hi", nil)
	cctx.AppendUser("second")

	snap := cctx.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(snap))
	}
	if snap[0].Content != "first" || snap[2].Content != "second" {
		t.Errorf("snapshot order broken: %+v", snap)
	}
}

// TestSnapshotAnchorsOnLatestSummary is the core compaction contract:
// the slice handed to the LLM begins at the most recent KindSummary
// marker. Older history stays on the Context for audit but is no longer
// sent.
func TestSnapshotAnchorsOnLatestSummary(t *testing.T) {
	t.Parallel()

	cctx := runtime.NewContext("sys", nil)
	// Pre-compact noise — issue triage, several tool turns, etc.
	cctx.AppendUser("event payload from old issue")
	cctx.AppendAssistant("looked at it", []llm.ToolCall{{ID: "tc_1", Name: "read", Arguments: "{}"}})
	cctx.AppendToolResult("tc_1", `{"content":"...lots of file body..."}`)
	cctx.AppendAssistant("done", nil)
	// LLM decides to compact.
	cctx.AppendSummary("Closed old issue #42 with commit abc1234. Working branch left clean. Next: pick up new event.")
	// New event arrives post-compact.
	cctx.AppendUser("event payload from new issue")
	cctx.AppendAssistant("starting on it", nil)

	snap := cctx.Snapshot()
	// 3 entries: summary, new event, assistant ack. The pre-compact
	// four are dropped from the LLM view but still in Context.
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3 (summary + new event + ack); got %+v", len(snap), snap)
	}
	if snap[0].Kind != llm.KindSummary {
		t.Errorf("snapshot[0].Kind = %q, want %q", snap[0].Kind, llm.KindSummary)
	}
	if snap[1].Content != "event payload from new issue" {
		t.Errorf("snapshot[1] not the post-compact event: %+v", snap[1])
	}
	// Underlying audit slice still has every message.
	if cctx.Len() != 7 {
		t.Errorf("ctx.Len() = %d, want 7 (audit retains pre-compact history)", cctx.Len())
	}
}

// TestSnapshotPicksMostRecentSummary covers the multi-compact case:
// when several summaries are present, only the freshest one anchors the
// window — older summaries are themselves part of the dropped tail.
func TestSnapshotPicksMostRecentSummary(t *testing.T) {
	t.Parallel()

	cctx := runtime.NewContext("sys", nil)
	cctx.AppendUser("a")
	cctx.AppendSummary("first compact")
	cctx.AppendUser("b")
	cctx.AppendSummary("second compact")
	cctx.AppendUser("c")

	snap := cctx.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	if snap[0].Content != "second compact" {
		t.Errorf("snapshot[0] = %q, want freshest summary", snap[0].Content)
	}
	if snap[1].Content != "c" {
		t.Errorf("snapshot[1] = %q, want \"c\"", snap[1].Content)
	}
}

// TestSnapshotSerialisesSummaryAsUserText pins the wire shape: a
// KindSummary entry must be emitted as a user-role message whose text
// is wrapped in <previous_session_summary> tags. The wrapper is what
// signals to the LLM that this block is authoritative context from a
// prior compacted segment rather than a fresh user prompt.
func TestSnapshotSerialisesSummaryAsUserText(t *testing.T) {
	t.Parallel()

	cctx := runtime.NewContext("sys", nil)
	cctx.AppendSummary("compact body here")
	cctx.AppendUser("next step")

	items := llm.ToInputItems(cctx.Snapshot())
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2; items=%+v", len(items), items)
	}
	first := items[0]
	if first["role"] != "user" {
		t.Errorf("summary item role = %v, want user", first["role"])
	}
	content, ok := first["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("summary item content shape: %+v", first["content"])
	}
	text, _ := content[0]["text"].(string)
	if want := "<previous_session_summary>\ncompact body here\n</previous_session_summary>"; text != want {
		t.Errorf("summary text = %q, want %q", text, want)
	}
}
