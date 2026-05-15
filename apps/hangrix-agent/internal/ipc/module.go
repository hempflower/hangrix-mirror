package ipc

import (
	"os"

	"github.com/hangrix/hangrix/pkg/ioc"
)

// ioc wiring lives here, in-package, so a consumer reading internal/ipc
// sees both the transport implementation and the providers it registers.
// Stdio is hard-wired because the agent is one-process-per-pod: the
// runner pipes us, and there is no use-case for swapping the transport
// at runtime. Tests that need different IO call NewReader/NewWriter
// directly against an in-memory pipe and skip the container.

func newReaderProvider() *Reader { return NewReader(os.Stdin) }
func newWriterProvider() *Writer { return NewWriter(os.Stdout) }

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(newReaderProvider).ToSelf()
	m.Provide(newWriterProvider).ToSelf()
	return m
}
