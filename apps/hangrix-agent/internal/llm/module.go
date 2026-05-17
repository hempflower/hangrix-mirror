package llm

import (
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/config"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// Deps lists what the LLM provider needs from the container. Reading
// from *config.Config (rather than accepting endpoint/token as scalar
// args directly) is required by ioc — the container only resolves
// pointer-to-struct and interface dependencies, not bare strings.
type Deps struct {
	Cfg *config.Config
}

// NewProvider is the ioc-shaped constructor. config.NewConfig has
// already panicked if HANGRIX_PLATFORM_BASE_URL or
// HANGRIX_SESSION_TOKEN is missing, so the values read here are
// guaranteed non-empty.
func NewProvider(deps *Deps) *Client {
	return New(deps.Cfg.LLMEndpoint(), deps.Cfg.SessionToken)
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewProvider).ToSelf()
	return m
}
