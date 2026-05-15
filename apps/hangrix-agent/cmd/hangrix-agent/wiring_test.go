package main

import (
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/app"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// TestBuildContainer pins the container's dependency graph: every
// module's Deps must resolve, the root *App must be retrievable, and
// the env validation in config.NewConfig must accept the required
// triplet (SESSION_TOKEN / LLM_ENDPOINT / LLM_MODEL). A new module
// whose Deps name an unregistered type will fail this test long before
// anyone deploys the binary.
func TestBuildContainer(t *testing.T) {
	t.Setenv("HANGRIX_SESSION_TOKEN", "tok")
	t.Setenv("HANGRIX_LLM_ENDPOINT", "http://llm.invalid")
	t.Setenv("HANGRIX_LLM_MODEL", "fake-model")
	// MCP endpoint left unset to exercise the typed-nil *mcp.Client
	// branch through toolregistry.
	t.Setenv("HANGRIX_PLATFORM_MCP_ENDPOINT", "")

	c := buildContainer()
	a := ioc.Get[*app.App](c)
	if a == nil {
		t.Fatal("buildContainer().Get(*App) returned nil")
	}
}
