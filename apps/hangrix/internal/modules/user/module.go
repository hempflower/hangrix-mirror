// Package user wires the user domain (Repo + HTTP handler). Other modules
// depend on domain.Repo via the ioc container; they must never import this
// module's handler or repo packages directly.
package user

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(infra.NewPostgresRepo).ToInterface(new(domain.Repo))
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
