package runtime

import (
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/ipc"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/prompt"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// Deps pulls in every direct dependency the runtime loop needs. This
// is the deepest node in the agent's dependency graph below *app.App:
// any module that wires something the loop reaches transitively must
// be loaded into the container before this one's NewProvider runs.
type Deps struct {
	Cfg       *config.Config
	Reader    *ipc.Reader
	Writer    *ipc.Writer
	LLM       *llm.Client
	Registry  *tools.Registry
	Assembled *prompt.Assembled
}

func NewProvider(deps *Deps) *Loop {
	return NewLoop(
		deps.Reader,
		deps.Writer,
		deps.LLM,
		deps.Cfg.Model,
		deps.Registry,
		deps.Assembled.Prompt,
	)
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewProvider).ToSelf()
	return m
}
