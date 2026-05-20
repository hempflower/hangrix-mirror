package main

import (
	"log"
	"os"

	"github.com/hangrix/hangrix/apps/hangrix/internal/app"
	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/kv"
	agentsession "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session"
	automation "github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/dashboard"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/git"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/healthz"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/hello"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue"
	llmprovider "github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider"
	llmproxy "github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_proxy"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/org"
	platformmcp "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_mcp"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/release"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/user"
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
		database.Module(),
		kv.Module(),
		healthz.Module(),
		hello.Module(),
		user.Module(),
		auth.Module(),
		token.Module(),
		git.Module(),
		org.Module(),
		repo.Module(),
		release.Module(),
		issue.Module(),
		llmprovider.Module(),
		llmproxy.Module(),
		runner.Module(),
		agentsession.Module(),
			dashboard.Module(),
		platformmcp.Module(),
		automation.Module(),
		web.Module(),
	)

	a := ioc.Get[*app.App](c)
	if err := a.Run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
