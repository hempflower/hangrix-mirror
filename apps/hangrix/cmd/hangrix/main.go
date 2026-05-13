package main

import (
	"log"
	"os"

	"github.com/hangrix/hangrix/apps/hangrix/internal/app"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/healthz"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/hello"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/apps/hangrix/internal/web"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func main() {
	c := ioc.NewContainer()
	c.Provide(func() *ioc.Container { return c }).ToSelf()
	c.Load(
		app.Module(),
		server.Module(),
		healthz.Module(),
		hello.Module(),
		web.Module(),
	)

	a := ioc.Get[*app.App](c)
	if err := a.Run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
