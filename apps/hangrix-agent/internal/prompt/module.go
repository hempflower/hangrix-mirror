package prompt

import (
	"fmt"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/config"
	"github.com/hangrix/hangrix/pkg/ioc"
)

type Deps struct {
	Cfg *config.Config
}

// NewProvider translates *config.Config into the prompt.Inputs that
// Assemble expects. Centralising the field-by-field translation here
// (rather than in main.go) means adding a new runtime-context field
// touches only this file and the Inputs struct — no caller-side churn.
//
// Assemble returns an error for "configured bundle is unreadable" /
// "host addendum path set but missing"; ioc constructors can't return
// errors, so we panic and let main.go's recover convert it back into
// the documented single-line stderr exit.
func NewProvider(deps *Deps) *Assembled {
	a, err := Assemble(Inputs{
		BundleDir:        deps.Cfg.BundleDir,
		HostAddendumPath: deps.Cfg.HostAddendumPath,
		Role:             deps.Cfg.Role,
		HostRepo:         deps.Cfg.HostRepo,
		IssueNumber:      deps.Cfg.IssueNumber,
		WorkingBranch:    deps.Cfg.WorkingBranch,
		BaseBranch:       deps.Cfg.BaseBranch,
		SessionID:        deps.Cfg.SessionID,
	})
	if err != nil {
		panic(fmt.Errorf("prompt: assemble: %w", err))
	}
	return a
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewProvider).ToSelf()
	return m
}
