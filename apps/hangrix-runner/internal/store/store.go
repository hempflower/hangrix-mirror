// Package store persists the runner's long-term identity + bootstrap
// snapshot locally — runner_id + agent_token + endpoints. One
// state.json file under the configured state dir.
//
// The agent token is the runner's long-term credential. It is stored
// plaintext on disk because the runner needs to send it on every poll;
// guard the file with 0600 perms. Treat the state directory the same
// way you would treat ~/.kube/config.
//
// The agent binary used to ride in here too via a Binaries map +
// content-addressed cache; that's gone now — the runner extracts an
// embedded `hangrix-agent` to <state-dir>/agent/ at serve-time.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// State is the durable snapshot the runner writes after enrollment and
// re-writes on each `serve` startup after refreshing the bootstrap.
type State struct {
	Server     string `json:"server"`
	RunnerID   int64  `json:"runner_id"`
	RunnerName string `json:"runner_name"`
	AgentToken string `json:"agent_token"`

	BaseURL string `json:"base_url"`

	DefaultAgentImage string `json:"default_agent_image,omitempty"`
	PollWaitSec       int    `json:"poll_wait_sec"`
	HeartbeatSec      int    `json:"heartbeat_sec"`
}

// ErrNotEnrolled is returned by Load when no state file exists. Used by
// the `serve` subcommand to print a clear "you must enroll first" hint
// rather than a generic "open file: not found".
var ErrNotEnrolled = errors.New("runner not enrolled (state.json missing)")

func filePath(dir string) string { return filepath.Join(dir, "state.json") }

// WorkspacesDir is the per-session host workdir root. Each session
// materialises a "session-<id>/" subdirectory; cleanup happens after
// the session terminates.
func WorkspacesDir(stateDir string) string {
	return filepath.Join(stateDir, "workspaces")
}

// LocalWorkspaceDir is the method form of WorkspacesDir, kept on the
// State so callers in cli/serve can pass an argument through one
// pointer instead of threading stateDir alongside.
func (s *State) LocalWorkspaceDir(stateDir string) string {
	return WorkspacesDir(stateDir)
}

func Load(dir string) (*State, error) {
	p := filePath(dir)
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotEnrolled
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if s.AgentToken == "" || s.RunnerID == 0 {
		return nil, fmt.Errorf("state.json incomplete")
	}
	return &s, nil
}

// Save writes the state atomically (write to .tmp + rename) so a crash
// mid-write does not leave a half-flushed file.
func Save(dir string, s *State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	p := filePath(dir)
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("write state tmp: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}
