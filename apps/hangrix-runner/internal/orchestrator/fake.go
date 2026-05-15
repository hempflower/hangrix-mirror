package orchestrator

import (
	"context"
	"io"
	"sync"
)

// FakeOrchestrator is the test double. It returns a Handle backed by
// in-memory pipes so a test can drive an "agent" by writing JSON-Lines
// frames into its stdout side and read whatever the runner sends to
// stdin off the other end of that pipe.
//
// Usage in tests:
//
//	fake := NewFake()
//	// arrange: spawn a goroutine that mimics the agent.
//	go func() {
//	    fake.AgentStdout().Write([]byte(`{"kind":"status","phase":"ready"}\n`))
//	    in := bufio.NewScanner(fake.AgentStdin())
//	    for in.Scan() { … }
//	    fake.Exit(0)
//	}()
//	loop.Run(ctx)  // claims a task → orch.Start(fake)
//
// Concurrency: Stop / Exit are safe to call from any goroutine.
type FakeOrchestrator struct {
	mu       sync.Mutex
	exitCode int
	waitCh   chan struct{}

	// stdin pipe: writer is the runner side, reader is the agent side.
	stdinR *io.PipeReader
	stdinW *io.PipeWriter
	// stdout pipe: writer is the agent side, reader is the runner side.
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	// stderr pipe: writer is the agent side, reader is the runner side.
	stderrR *io.PipeReader
	stderrW *io.PipeWriter

	lastTask Task
}

func NewFake() *FakeOrchestrator {
	f := &FakeOrchestrator{waitCh: make(chan struct{})}
	f.stdinR, f.stdinW = io.Pipe()
	f.stdoutR, f.stdoutW = io.Pipe()
	f.stderrR, f.stderrW = io.Pipe()
	return f
}

func (f *FakeOrchestrator) Start(ctx context.Context, t Task) (Handle, error) {
	f.mu.Lock()
	f.lastTask = t
	f.mu.Unlock()
	return &fakeHandle{f: f}, nil
}

// AgentStdin is the read-side of the runner→agent pipe. Tests treat it
// as the "agent's stdin" — read frames the runner ships.
func (f *FakeOrchestrator) AgentStdin() io.Reader { return f.stdinR }

// AgentStdout is the write-side of the agent→runner pipe. Tests write
// frames here; the runner reads them via Handle.Stdout().
func (f *FakeOrchestrator) AgentStdout() io.WriteCloser { return f.stdoutW }

// AgentStderr is the write-side of stderr pipe; tests use this to emit
// log lines the runner forwards as `log` frames.
func (f *FakeOrchestrator) AgentStderr() io.WriteCloser { return f.stderrW }

// Exit causes Wait to return with the given code. Idempotent.
func (f *FakeOrchestrator) Exit(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.waitCh:
		return
	default:
	}
	f.exitCode = code
	// Close pipes so any pending reader/writer unblocks.
	_ = f.stdoutW.Close()
	_ = f.stderrW.Close()
	_ = f.stdinR.Close()
	close(f.waitCh)
}

// LastTask returns whatever Start most recently received. Tests assert
// against this to verify env / bind-mount plumbing.
func (f *FakeOrchestrator) LastTask() Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastTask
}

type fakeHandle struct {
	f *FakeOrchestrator
}

func (h *fakeHandle) Stdin() io.WriteCloser { return h.f.stdinW }
func (h *fakeHandle) Stdout() io.Reader     { return h.f.stdoutR }
func (h *fakeHandle) Stderr() io.Reader     { return h.f.stderrR }

func (h *fakeHandle) Wait() (int, error) {
	<-h.f.waitCh
	return h.f.exitCode, nil
}

func (h *fakeHandle) Stop(ctx context.Context) error {
	h.f.Exit(0)
	return nil
}
