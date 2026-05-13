// Package git wires the git module: the go-git-backed infra implementation
// is bound to the domain.Git interface so other modules can depend on it
// through the ioc container without importing infra directly.
package git

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/infra"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(infra.NewGoGit).ToInterface(new(domain.Git))
	return m
}
