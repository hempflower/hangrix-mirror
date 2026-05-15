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
)

// Tool is the in-process tool contract. All seven local tools implement
// it. The registry adapts these to the LLM's function-tool schema and to
// the runtime's "tool call → result" plumbing.
type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any                                              // JSON-Schema for parameters
	Call(ctx context.Context, args json.RawMessage) (any, error)         // returns JSON-serialisable result
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

// All returns the canonical local tool catalogue. Order matches the spec
// table in ROADMAP.md so the registry-emitted catalogue is deterministic
// and the tool-allowlist filter behaves predictably.
func All() []Tool {
	tracker := NewReadTracker()
	bash := newBashTool()
	return []Tool{
		newReadTool(tracker),
		newWriteTool(),
		newEditTool(tracker),
		newGlobTool(),
		newGrepTool(),
		bash,
		newWebFetchTool(),
	}
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
