package prompt_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/prompt"
)

// TestAssemble_AllThreeLayers spreads a temporary agent bundle + host
// addendum on disk and verifies all three layers compose in the
// documented order. The test owns the layer ordering contract — if a
// future refactor accidentally swaps "agent base" and "host addendum",
// this fires.
func TestAssemble_AllThreeLayers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.yml"),
		[]byte("name: dispatcher\nentry:\n  base_prompt: prompts/dispatcher.md\n"), 0o644); err != nil {
		t.Fatalf("write agent.yml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "dispatcher.md"),
		[]byte("AGENT_LAYER_MARKER\n"), 0o644); err != nil {
		t.Fatalf("write dispatcher.md: %v", err)
	}
	hostFile := filepath.Join(dir, "host.md")
	if err := os.WriteFile(hostFile, []byte("HOST_LAYER_MARKER\n"), 0o644); err != nil {
		t.Fatalf("write host: %v", err)
	}

	a, err := prompt.Assemble(prompt.Inputs{
		BundleDir:        dir,
		HostAddendumPath: hostFile,
		Role:             "dispatcher",
		HostRepo:         "alice/repo",
		IssueNumber:      "7",
	})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if want := []string{"baseline", "agent", "host"}; !equalSlices(a.KeptLayers, want) {
		t.Errorf("kept layers = %v, want %v", a.KeptLayers, want)
	}
	// Ordering matters: baseline (compiled-in heading) before agent
	// before host.
	baselineIdx := strings.Index(a.Prompt, "Hangrix agent runtime baseline")
	agentIdx := strings.Index(a.Prompt, "AGENT_LAYER_MARKER")
	hostIdx := strings.Index(a.Prompt, "HOST_LAYER_MARKER")
	if baselineIdx < 0 || agentIdx < 0 || hostIdx < 0 {
		t.Fatalf("missing layer markers (baseline=%d agent=%d host=%d)\n%s",
			baselineIdx, agentIdx, hostIdx, a.Prompt)
	}
	if !(baselineIdx < agentIdx && agentIdx < hostIdx) {
		t.Errorf("layer order broken: baseline=%d agent=%d host=%d", baselineIdx, agentIdx, hostIdx)
	}
	if !strings.Contains(a.Prompt, "role: dispatcher") {
		t.Errorf("runtime context not surfaced: %s", a.Prompt[:200])
	}
}

func TestAssemble_BaselineOnly(t *testing.T) {
	t.Parallel()
	a, err := prompt.Assemble(prompt.Inputs{Role: "test"})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if !equalSlices(a.KeptLayers, []string{"baseline"}) {
		t.Errorf("kept layers = %v, want [baseline]", a.KeptLayers)
	}
	if !strings.Contains(a.Prompt, "Hangrix agent runtime baseline") {
		t.Error("baseline content missing")
	}
}

// TestAssemble_BadBundle: a configured bundle that we can't read should
// surface as an error (loud) rather than fall back to "baseline only"
// (silent). A typo in the runner's bundle path should not produce an
// agent that runs but acts wrong.
func TestAssemble_BadBundle(t *testing.T) {
	t.Parallel()
	_, err := prompt.Assemble(prompt.Inputs{BundleDir: "/no/such/bundle/path"})
	if err == nil {
		t.Fatal("expected error for missing bundle dir")
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
