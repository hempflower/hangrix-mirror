package prompt_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/prompt"
)

// TestAssemble_BothLayers writes a host addendum on disk and verifies
// baseline + host compose in the documented order. The test owns the
// layer ordering contract — if a future refactor accidentally swaps
// baseline and host, this fires.
func TestAssemble_BothLayers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	hostFile := filepath.Join(dir, "host.md")
	if err := os.WriteFile(hostFile, []byte("HOST_LAYER_MARKER\n"), 0o644); err != nil {
		t.Fatalf("write host: %v", err)
	}

	a, err := prompt.Assemble(prompt.Inputs{
		HostAddendumPath: hostFile,
		Role:             "dispatcher",
		HostRepo:         "alice/repo",
		IssueNumber:      "7",
	})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if want := []string{"baseline", "host"}; !equalSlices(a.KeptLayers, want) {
		t.Errorf("kept layers = %v, want %v", a.KeptLayers, want)
	}
	baselineIdx := strings.Index(a.Prompt, "Hangrix agent runtime baseline")
	hostIdx := strings.Index(a.Prompt, "HOST_LAYER_MARKER")
	if baselineIdx < 0 || hostIdx < 0 {
		t.Fatalf("missing layer markers (baseline=%d host=%d)\n%s",
			baselineIdx, hostIdx, a.Prompt)
	}
	if !(baselineIdx < hostIdx) {
		t.Errorf("layer order broken: baseline=%d host=%d", baselineIdx, hostIdx)
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

// TestAssemble_BadHostPath: a configured host addendum path we can't
// read should surface as an error (loud) rather than fall back to
// "baseline only" (silent). A typo in the runner's bind-mount path
// must not produce an agent that runs without the role's prompt.
func TestAssemble_BadHostPath(t *testing.T) {
	t.Parallel()
	_, err := prompt.Assemble(prompt.Inputs{HostAddendumPath: "/no/such/path"})
	if err == nil {
		t.Fatal("expected error for missing host addendum path")
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
