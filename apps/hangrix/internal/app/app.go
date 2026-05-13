package app

import (
	"flag"
	"fmt"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// App is resolved from the ioc container by main. It owns the runtime
// lifecycle: parsing CLI args, loading config, registering it back into the
// container, then resolving and starting the server.
type App struct {
	container *ioc.Container
}

type AppDeps struct {
	Container *ioc.Container
}

func NewApp(deps *AppDeps) *App {
	return &App{container: deps.Container}
}

func (a *App) Run(args []string) error {
	fs := flag.NewFlagSet("hangrix", flag.ExitOnError)
	configPath := fs.String("config", "conf/config.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	a.container.Provide(func() *config.Config { return cfg }).ToSelf()

	srv := ioc.Get[*server.Server](a.container)
	return srv.ListenAndServe()
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewApp).ToSelf()
	return m
}
