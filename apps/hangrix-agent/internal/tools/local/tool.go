// Package local implements the in-process tool catalogue the agent
// invokes inside the container: read / write / edit / glob / grep / bash
// / webfetch. Each tool follows the Tool interface and is registered into
// the catalogue at agent startup.
//
// Design notes:
//   - Tools return JSON-serialisable Go values, not strings. The caller
//     (registry) marshals them once for both the LLM round-trip and the
//     IPC tool_call frame, avoiding a "decode then re-encode" pass.
//   - Path arguments are taken at face value. The container is the
//     sandbox; we don't try to confine to /workspace ourselves because
//     the LLM legitimately needs to touch /tmp, $HANGRIX_AGENT_BUNDLE,
//     and $HANGRIX_HOST_ADDENDUM. Confinement belongs to the runner's
//     container config (read-only mounts, mount-points), not here.
//   - ReadTracker is shared across tools so `edit` can enforce
//     read-before-write within one session.
//
// Error message convention. Tool errors are fed directly back to the LLM
// as the function-call output, so they double as documentation. Each
// error SHOULD follow a three-part shape — "<tool>: <what went wrong>.
// <Why the rule exists / what the constraint is>. <How to recover>." —
// so the model can self-correct without having to re-derive the tool's
// contract. Bare OS errors (file-not-found, permission denied) are
// passed through when the message already explains itself; everything
// validation- or contract-related uses the three-part shape.
package local

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Tool is the in-process tool contract. All seven local tools implement
// it. The registry adapts these to the LLM's function-tool schema and to
// the runtime's "tool call → result" plumbing.
type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any                                      // JSON-Schema for parameters
	Call(ctx context.Context, args json.RawMessage) (any, error) // returns JSON-serialisable result
}

// ReadTracker remembers which paths have been read in the current
// session so `edit` can refuse to mutate a file the LLM has not yet
// inspected. Concurrency-safe so future parallel tool execution doesn't
// race here.
type ReadTracker struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func NewReadTracker() *ReadTracker {
	return &ReadTracker{seen: map[string]struct{}{}}
}

func (r *ReadTracker) MarkRead(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen[path] = struct{}{}
}

func (r *ReadTracker) WasRead(path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.seen[path]
	return ok
}

// AsyncLifecycle is the public surface the runtime uses to observe and
// shut down local async work (background bash tasks, sleep timers, and
// future async tools). It's separate from the Tool interface because
// these are operations the *agent runtime* drives — not things the LLM
// invokes through a tool call. Exposed as an interface so tests can
// substitute a no-op implementation when they don't care about async
// work.
type AsyncLifecycle interface {
	// NotificationCh streams one user-role text snippet per completed
	// async work item. The runtime drains it at every "wait point"
	// (idle, between LLM rounds) and appends the messages into the
	// conversation so the LLM doesn't have to poll to learn something
	// finished.
	NotificationCh() <-chan string
	// HasRunningJobs reports the count of still-alive async work items
	// (background bash jobs + pending sleep timers). Used to populate
	// the `running_jobs` hint on the agent's `idle` outbound frame.
	HasRunningJobs() int
	// Cleanup cancels every alive job / pending timer and waits, bounded
	// by the supplied context, for them to reap. Called on agent shutdown.
	Cleanup(ctx context.Context)
	// Schedule fires a notification after the given duration. Returns an
	// opaque ID usable with CancelSchedule. The notification text is sent
	// to NotificationCh when the timer fires. Schedule increments the
	// running-job count; it decrements on fire or cancel.
	Schedule(d time.Duration, notification string) string
	// ScheduleWithID is like Schedule but uses the caller-provided id
	// instead of generating one. This allows the caller to embed the id
	// in the notification text (e.g. a sleep tool including sleep_id in
	// the completion message for disambiguation).
	ScheduleWithID(id string, d time.Duration, notification string)
	// CancelSchedule cancels a previously scheduled notification. No
	// notification is sent. Safe to call on an already-fired ID (no-op).
	CancelSchedule(id string)
}

// Bundle is the result of Build: every Tool the agent registers plus
// the AsyncLifecycle handle the runtime hooks into. Returning them
// together (instead of having callers fish the concrete type out of the
// Tool slice and downcast) keeps the wiring honest — the runtime gets
// exactly the verbs it needs, nothing more.
type Bundle struct {
	Tools []Tool
	Async AsyncLifecycle
}

// Build is the canonical constructor: one ReadTracker shared across
// read/edit, one bashTool shared between `bash` and `bash_input` (so
// they see the same background-job map). Callers that need both halves
// (production wiring via ioc) use Build; callers that only need the
// Tool slice (tests) use All, which
// is a thin wrapper.
//
// Order matches the spec table in ROADMAP.md so the registry-emitted
// catalogue is deterministic and the tool-allowlist filter behaves
// predictably. The `research` tool was previously appended by
// BuildWithResearch / AllWithResearch in research.go; those helpers
// and the tool itself have been removed.
func Build() Bundle {
	tracker := NewReadTracker()
	bash := newBashTool()
	registry := NewFormatterRegistry()
	return Bundle{
		Tools: []Tool{
			newReadTool(tracker),
			newWriteTool(),
			newEditTool(tracker, registry),
			newGlobTool(),
			newGrepTool(),
			bash,
			// bash_input sits right next to bash on purpose: they share
			// the background-job map, and the LLM almost always reaches
			// for them as a pair (start a long-running command, then
			// feed it stdin).
			newBashInputTool(bash),
			newWebFetchTool(),
			newSleepTool(bash),
			// compact_session is a schema-only stub — the runtime loop
			// intercepts the call by name and applies its effect to the
			// in-memory Context. Registering it here makes the
			// descriptor flow through the standard catalogue/allowlist
			// path so role configs can opt out symmetrically.
			NewCompactSessionTool(),
		},
		Async: bash,
	}
}

// All returns just the Tool slice from Build. Retained for callers
// that don't need the AsyncLifecycle handle (notably: tests, which
// don't want to manage background jobs).
func All() []Tool {
	return Build().Tools
}

// decodeArgs is a small convenience for tool implementations: it
// json.Unmarshals into the given destination and converts any error into
// an InvalidArgs sentinel. Tools should report InvalidArgs to the LLM so
// it can self-correct rather than retry blindly.
func decodeArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}
