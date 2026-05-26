// Package service implements the agent_session module's three orchestration
// surfaces:
//
//   - Spawner   — host yaml → per-role agent_sessions rows
//   - Archiver  — issue.closed / issue.merged → sessions to 'archived'
//   - Auditor   — list sessions / messages on (repo, issue) for the audit chain
//
// Everything stateful lives behind a small interface (HostBlobReader,
// RunnerPicker, runner.Repo) so the unit tests in service/*_test.go can
// stub out git + DB and still exercise the full happy path.
package service

import (
	"context"
	"io"
	"os/exec"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
)

// GitBlobReader reads <ref>:<path> via `git cat-file -p`. Mirrors the
// readBlobAtRef helper that already lives in repo/handler/git_http.go,
// re-implemented here so the agent_session module doesn't import the repo
// handler package. The same swallow-stderr / (nil,false)-on-error stance:
// callers treat "no blob" as "host repo has no `.hangrix/agents.yml`",
// which is a perfectly valid state for a non-agent host.
type GitBlobReader struct{}

// NewGitBlobReader returns a ready-to-use reader. Zero state, no deps.
func NewGitBlobReader() *GitBlobReader { return &GitBlobReader{} }

// ReadBlob satisfies domain.HostBlobReader. repoFsPath is the bare repo
// path on disk (the runner module already resolves this via
// repo.PathResolver.ResolvePath).
func (r *GitBlobReader) ReadBlob(ctx context.Context, repoFsPath, ref, path string) ([]byte, bool) {
	cmd := exec.CommandContext(ctx,
		"git",
		"--git-dir="+repoFsPath,
		"cat-file",
		"-p",
		ref+":"+path,
	)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

// ListBlobs satisfies domain.HostBlobReader. It returns the repo-relative
// paths of the entries directly under <ref>:<dir> via
// `git ls-tree --name-only <ref> <dir>/`. (nil, false) when the directory
// does not exist at that ref (the trailing slash makes git list the
// directory's children rather than the dir entry itself).
func (r *GitBlobReader) ListBlobs(ctx context.Context, repoFsPath, ref, dir string) ([]string, bool) {
	cmd := exec.CommandContext(ctx,
		"git",
		"--git-dir="+repoFsPath,
		"ls-tree",
		"--name-only",
		ref,
		strings.TrimSuffix(dir, "/")+"/",
	)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		return nil, false
	}
	return paths, true
}

// compile-time check
var _ domain.HostBlobReader = (*GitBlobReader)(nil)
