package service

import (
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
)

func TestResolveImageTag_Image(t *testing.T) {
	t.Parallel()
	got, err := resolveImageTag(7, agentsconfig.Container{Image: "ghcr.io/acme/dev:1.0"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "ghcr.io/acme/dev:1.0" {
		t.Fatalf("image passthrough: %q", got)
	}
}

func TestResolveImageTag_BuildDeterministic(t *testing.T) {
	t.Parallel()
	build := &agentsconfig.Build{
		Dockerfile: ".hangrix/agent.Dockerfile",
		Context:    ".",
		Args:       map[string]string{"GO_VERSION": "1.26", "NODE_MAJOR": "20"},
	}
	c := agentsconfig.Container{Build: build}

	first, err := resolveImageTag(7, c)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	second, _ := resolveImageTag(7, c)
	if first != second {
		t.Fatalf("not deterministic: %q vs %q", first, second)
	}
	if first == "" {
		t.Fatalf("empty tag")
	}
	// Different repo id → different tag.
	other, _ := resolveImageTag(8, c)
	if other == first {
		t.Fatalf("repo id should namespace: %q == %q", other, first)
	}
	// Different arg → different tag.
	c2 := c
	c2.Build = &agentsconfig.Build{
		Dockerfile: build.Dockerfile,
		Context:    build.Context,
		Args:       map[string]string{"GO_VERSION": "1.27", "NODE_MAJOR": "20"},
	}
	mutated, _ := resolveImageTag(7, c2)
	if mutated == first {
		t.Fatalf("build arg change should yield new tag: %q == %q", mutated, first)
	}
}

func TestResolveImageTag_NeitherSet(t *testing.T) {
	t.Parallel()
	_, err := resolveImageTag(7, agentsconfig.Container{})
	if err == nil {
		t.Fatalf("expected error when neither image nor build set")
	}
}
