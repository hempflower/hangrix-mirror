package ipc_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/ipc"
)

// TestReader_RoundsTrip walks the three inbound shapes the runner emits
// and verifies they decode into the right discriminated fields. A
// regression here usually means the IPC contract drifted from the
// runner's spec without us updating both ends in lockstep.
func TestReader_RoundsTrip(t *testing.T) {
	t.Parallel()

	stream := strings.Join([]string{
		`{"kind":"history","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hi back"}]}`,
		`{"kind":"event","event":"issue.comment.mentioned","payload":{"comment_id":42}}`,
		``, // blank line — should be skipped, not treated as EOF
		`{"kind":"control","op":"shutdown"}`,
	}, "\n") + "\n"

	r := ipc.NewReader(strings.NewReader(stream))

	first, err := r.Read()
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if first.Kind != "history" || len(first.Messages) != 2 {
		t.Fatalf("history frame mis-parsed: %+v", first)
	}

	second, err := r.Read()
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if second.Kind != "event" || second.Event != "issue.comment.mentioned" {
		t.Fatalf("event frame mis-parsed: %+v", second)
	}
	if !bytes.Contains(second.Payload, []byte(`"comment_id":42`)) {
		t.Fatalf("payload not preserved verbatim: %s", second.Payload)
	}

	third, err := r.Read()
	if err != nil {
		t.Fatalf("third read: %v", err)
	}
	if third.Kind != "control" || third.Op != "shutdown" {
		t.Fatalf("control frame mis-parsed: %+v", third)
	}

	if _, err := r.Read(); err != io.EOF {
		t.Fatalf("expected EOF after 3 frames, got %v", err)
	}
}

// TestReader_MissingKind enforces our "every frame must declare a kind"
// invariant. The runner has more freedom in how it constructs frames
// than we'd like; without this check a `{}`-only line would silently
// no-op and stall the loop.
func TestReader_MissingKind(t *testing.T) {
	t.Parallel()
	r := ipc.NewReader(strings.NewReader(`{"messages":[]}` + "\n"))
	if _, err := r.Read(); err == nil {
		t.Fatal("expected error for kind-less frame")
	}
}

// TestWriter_FrameOrdering ensures concurrent writes don't interleave.
// The runtime emits status / log / message from the same goroutine
// today, but tools may grow goroutines later (e.g. background bash
// streaming partial output) — this test pins the contract.
func TestWriter_FrameOrdering(t *testing.T) {
	t.Parallel()
	var buf threadSafeBuffer
	w := ipc.NewWriter(&buf)

	const n = 50
	done := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			_ = w.Status("thinking")
		}
		close(done)
	}()
	for i := 0; i < n; i++ {
		_ = w.Log("info", "from main")
	}
	<-done

	// Each line must be a valid JSON object terminated by a newline.
	// Counting "}\n" gives us total frames; mismatched count means a
	// write was torn.
	if got := bytes.Count(buf.Bytes(), []byte("}\n")); got != 2*n {
		t.Fatalf("expected %d frames, got %d", 2*n, got)
	}
}

type threadSafeBuffer struct {
	bytes.Buffer
}

// (*bytes.Buffer).Write isn't goroutine safe; the writer's mutex is the
// thing being tested, so we wrap with a no-op shim purely so the test
// compiles. The actual concurrency safety lives in ipc.Writer.

func (b *threadSafeBuffer) Write(p []byte) (int, error) { return b.Buffer.Write(p) }
