package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateContainerVolumes verifies that createContainer appends a
// `-v name:/mount` argument for every volume in the task, after the
// built-in bind mounts and before the image name.
func TestCreateContainerVolumes(t *testing.T) {
	// Build a fake docker script that records its "create" arguments
	// to a temp file so we can inspect them after the call.
	recFile := filepath.Join(t.TempDir(), "docker-create-args.txt")

	fakeDocker := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/bash
case "$1" in
  create)
    shift
    # Write all remaining args (one per line) to the record file.
    printf '%s\n' "$@" > ` + recFile + `
    echo "fake-container-voltest"
    ;;
  start | image)
    # No-op — start + image inspect succeed silently.
    ;;
  *)
    echo "unexpected docker command: $*" >&2
    exit 1
    ;;
esac
exit 0
`
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Workdir must exist (Start calls os.MkdirAll) and AgentBinaryPath
	// must be a regular file (Start calls os.Stat + IsRegular).
	workdir := t.TempDir()
	agentBin := filepath.Join(t.TempDir(), "hangrix-agent")
	if err := os.WriteFile(agentBin, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	orch := &DockerOrchestrator{
		bin:     fakeDocker,
		network: "bridge",
	}

	task := Task{
		SessionID:       1,
		Image:           "alpine:latest",
		AgentBinaryPath: agentBin,
		HostWorkdir:     workdir,
		Volumes: []Volume{
			{Name: "npm-cache", Mount: "/root/.npm"},
			{Name: "go-build-cache", Mount: "/root/.cache/go-build"},
		},
	}

	_, err := orch.createContainer(context.Background(), task)
	if err != nil {
		t.Fatalf("createContainer: %v", err)
	}

	// Read back the recorded docker create arguments.
	data, err := os.ReadFile(recFile)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	rawArgs := strings.TrimSpace(string(data))
	args := strings.Split(rawArgs, "\n")

	// Find all -v arguments in the docker create invocation.
	var volArgs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-v" && i+1 < len(args) {
			volArgs = append(volArgs, args[i+1])
			i++
		}
	}

	// We expect at least the two built-in bind mounts + our two volumes = 4.
	if len(volArgs) < 4 {
		t.Fatalf("expected at least 4 -v args (2 bind mounts + 2 volumes), got %d: %v", len(volArgs), volArgs)
	}

	// The last two -v args should be our named volumes.
	// Check they appear after the built-in bind mounts and before the image name.
	got1 := volArgs[len(volArgs)-2]
	got2 := volArgs[len(volArgs)-1]

	if got1 != "npm-cache:/root/.npm" {
		t.Errorf("volume 1 = %q, want %q", got1, "npm-cache:/root/.npm")
	}
	if got2 != "go-build-cache:/root/.cache/go-build" {
		t.Errorf("volume 2 = %q, want %q", got2, "go-build-cache:/root/.cache/go-build")
	}

	// Verify that the image name appears after the last volume -v arg.
	// The docker create args end with: ... -v vol2 image cmd...
	imgIdx := -1
	for i, a := range args {
		if a == task.Image {
			imgIdx = i
			break
		}
	}
	if imgIdx < 0 {
		t.Fatalf("image %q not found in docker create args", task.Image)
	}
	lastVolIdx := -1
	for i := len(args) - 1; i >= 0; i-- {
		if args[i] == "-v" {
			lastVolIdx = i + 1 // index of the value after -v
			break
		}
	}
	if lastVolIdx < 0 || imgIdx <= lastVolIdx {
		t.Errorf("image %q appears at index %d, must be after last -v value at index %d", task.Image, imgIdx, lastVolIdx)
	}
}
