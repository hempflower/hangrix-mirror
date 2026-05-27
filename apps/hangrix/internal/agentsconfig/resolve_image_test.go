package agentsconfig

import "testing"

func TestResolveImageTag_Image(t *testing.T) {
	t.Parallel()
	got, err := ResolveImageTag(7, Container{Image: "ghcr.io/acme/dev:1.0"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "ghcr.io/acme/dev:1.0" {
		t.Fatalf("image passthrough: %q", got)
	}
}

func TestResolveImageTag_BuildDeterministic(t *testing.T) {
	t.Parallel()
	build := &Build{
		Dockerfile: ".hangrix/agent.Dockerfile",
		Context:    ".",
		Args:       map[string]string{"GO_VERSION": "1.26", "NODE_MAJOR": "20"},
	}
	c := Container{Build: build}

	first, err := ResolveImageTag(7, c)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	second, _ := ResolveImageTag(7, c)
	if first != second {
		t.Fatalf("not deterministic: %q vs %q", first, second)
	}
	if first == "" {
		t.Fatalf("empty tag")
	}
	// Different repo id → different tag.
	other, _ := ResolveImageTag(8, c)
	if other == first {
		t.Fatalf("repo id should namespace: %q == %q", other, first)
	}
	// Different arg → different tag.
	c2 := c
	c2.Build = &Build{
		Dockerfile: build.Dockerfile,
		Context:    build.Context,
		Args:       map[string]string{"GO_VERSION": "1.27", "NODE_MAJOR": "20"},
	}
	mutated, _ := ResolveImageTag(7, c2)
	if mutated == first {
		t.Fatalf("build arg change should yield new tag: %q == %q", mutated, first)
	}
}

func TestResolveImageTag_NeitherSet(t *testing.T) {
	t.Parallel()
	_, err := ResolveImageTag(7, Container{})
	if err == nil {
		t.Fatalf("expected error when neither image nor build set")
	}
}
