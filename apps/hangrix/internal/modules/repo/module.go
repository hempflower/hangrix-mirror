// Package repo wires the repo module: domain.Store (Postgres), the Storage
// filesystem helper, and the HTTP handler. Other modules depend only on
// domain.Store via the ioc container; they must never import infra or
// handler directly.
package repo

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(infra.NewPostgresStore).ToInterface(new(domain.Store))
	m.Provide(infra.NewPostgresProtectionStore).ToInterface(new(domain.ProtectionStore))
	storage := m.Provide(infra.NewStorage)
	storage.ToSelf()
	// Expose the path resolver as a narrow domain interface so the M7a
	// runner agent-bundles endpoint can compute fsPaths without taking
	// a hard dep on the concrete *infra.Storage.
	storage.ToInterface(new(domain.PathResolver))
	// M7a Phase 2: kind reclassification on default-branch tip change.
	// Both receive-pack post-receive AND issue merge consume this through
	// the narrow domain interface so the issue module doesn't have to
	// import repo/service or repo/handler.
	m.Provide(service.NewKindRefresher).ToInterface(new(domain.KindRefresher))
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
