package loop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// cloneSpec captures everything cloneRepo needs to materialise a host
// repo working tree at hostWorkdir/repo before the agent container
// starts. Pulled out of the inline SessionDriver code so the
// preparation logic is testable on its own.
type cloneSpec struct {
	BaseURL       string // platform base, e.g. https://hangrix.example
	Owner         string // host repo owner
	Name          string // host repo name
	WorkingBranch string // e.g. "issue/42"
	BaseBranch    string // fallback branch when working branch doesn't exist remotely
	SessionToken  string // hgxs_* plaintext — HTTP Basic password for the git server
	WorkflowToken string // hangrix_wf_* plaintext — HTTP Basic password for workflow-job clones
	Dest          string // absolute host path; created if missing, wiped if re-cloning
}

// validate is paranoia: any blank required field would otherwise turn
// into a confusing `git` error.
func (s cloneSpec) validate() error {
	if s.BaseURL == "" {
		return errors.New("clone: BaseURL is required")
	}
	if s.Owner == "" || s.Name == "" {
		return errors.New("clone: Owner and Name are required")
	}
	if s.SessionToken == "" {
		return errors.New("clone: SessionToken is required")
	}
	if s.Dest == "" {
		return errors.New("clone: Dest is required")
	}
	return nil
}

// gitURL builds the smart-HTTP URL for the host repo. The git server
// (apps/hangrix/internal/modules/repo/handler/git_http.go) mounts it
// under `/git/{owner}/{name}.git`.
func (s cloneSpec) gitURL() string {
	return strings.TrimRight(s.BaseURL, "/") + "/git/" + s.Owner + "/" + s.Name + ".git"
}

// credentialHelperConfigArg returns the `--config key=value` argument
// that wires a per-host inline credential helper into the cloned repo.
// The helper is a tiny shell snippet that prints HTTP Basic creds to
// stdout when git asks for them; it reads the session token from the
// HANGRIX_SESSION_TOKEN env var at request time rather than baking it
// into .git/config. That means a rotated / refreshed token (whether
// from a future rotation feature or just rewake re-injecting the same
// value) is picked up automatically — no .git/config rewrite, no
// container rebuild.
//
// Scoping: the section is `credential.<BaseURL>.helper`, so the helper
// only fires for requests targeting the Hangrix platform. If the agent
// happens to clone github.com (or anything else) the helper won't run
// and our session token doesn't leak to third parties.
//
// Git server's identifyGitCaller accepts HTTP Basic with the session
// token as the password and any username ("x" by convention).
func (s cloneSpec) credentialHelperConfigArg() string {
	base := strings.TrimRight(s.BaseURL, "/")
	// !... = run as shell. Use a function so the `; f` invocation
	// pattern matches the canonical example in gitcredentials(7) and
	// keeps the quoting simple. Double quotes around the variable
	// guard against tokens that ever grow shell-metacharacters; today
	// the wire format is [A-Za-z0-9_] so it's belt-and-braces.
	helper := `!f() { echo username=x; echo "password=$HANGRIX_SESSION_TOKEN"; }; f`
	return "credential." + base + ".helper=" + helper
}

// workflowCredentialHelperConfigArg is the workflow-job counterpart of
// credentialHelperConfigArg: same per-host inline helper shape, but it
// reads the run's workflow token (HANGRIX_WORKFLOW_TOKEN) instead of a
// session token. The git server's identifyGitCaller accepts a
// `hangrix_wf_*` Basic password for read-only access to the run's repo,
// which is what a workflow job needs to clone a private host repo.
func (s cloneSpec) workflowCredentialHelperConfigArg() string {
	base := strings.TrimRight(s.BaseURL, "/")
	helper := `!f() { echo username=x; echo "password=$HANGRIX_WORKFLOW_TOKEN"; }; f`
	return "credential." + base + ".helper=" + helper
}

// cloneRepo prepares the working tree at spec.Dest:
//
//  1. If spec.Dest already exists, wipe it. cloneRepo only runs when
//     the session has no container yet (see SessionDriver.Run); any
//     directory left at spec.Dest is therefore the residue of a
//     previous failed attempt — half-finished clone, half-applied
//     turn that never produced a container_id, etc. We deliberately
//     don't fetch-and-reset in place: the savings are small for
//     typical issue repos and the failure modes (corrupt .git after a
//     kill -9, leftover untracked files) are nasty to debug. Once the
//     container exists, the workdir is owned by it and the runner
//     must never touch this path again until the container is
//     deleted; otherwise the in-container bind mount onto the dir
//     inode goes stale and runc rejects the next `docker exec` with
//     `current working directory is outside of container mount
//     namespace root`.
//  2. `git clone` the host repo with a per-host credential.helper
//     baked into .git/config. The helper is an inline shell snippet
//     that reads $HANGRIX_SESSION_TOKEN at request time, so the
//     agent's later `git push` / `git fetch` from inside the container
//     pick up whatever token the runner injected into the agent's env
//     for the current turn — no .git/config rewrite needed if the
//     token ever rotates.
//  3. Try to check out spec.WorkingBranch. If origin already has it
//     (mid-issue work, previous agent already pushed), check out the
//     remote ref. Otherwise branch fresh from spec.BaseBranch — the
//     first agent run on a new issue creates the working branch
//     locally; it'll appear on origin the first time the agent pushes.
//
// Returns the path the container should bind-mount as /workspace
// (which is spec.Dest itself; we don't add a subdirectory).
func cloneRepo(ctx context.Context, spec cloneSpec) (string, error) {
	if err := spec.validate(); err != nil {
		return "", err
	}
	if err := os.RemoveAll(spec.Dest); err != nil {
		return "", fmt.Errorf("clone: clear dest %s: %w", spec.Dest, err)
	}
	if err := os.MkdirAll(filepath.Dir(spec.Dest), 0o755); err != nil {
		return "", fmt.Errorf("clone: ensure parent of %s: %w", spec.Dest, err)
	}

	// `git clone --config` writes the credential.<host>.helper entry
	// into the new repo's .git/config *before* the remote fetch, so
	// the same helper covers both the initial clone (running on the
	// runner host with HANGRIX_SESSION_TOKEN in its subprocess env)
	// and later fetch/push from inside the container (which has the
	// same env var injected by the orchestrator).
	cloneArgs := []string{
		"clone",
		"--config", spec.credentialHelperConfigArg(),
		"--branch", branchOrDefault(spec.BaseBranch, "main"),
		"--",
		spec.gitURL(),
		spec.Dest,
	}
	if err := runGitWithEnv(ctx, "", []string{"HANGRIX_SESSION_TOKEN=" + spec.SessionToken}, cloneArgs...); err != nil {
		return "", fmt.Errorf("clone %s: %w", spec.gitURL(), err)
	}

	// Working branch checkout. Empty WorkingBranch is fine — the
	// clone already left HEAD on BaseBranch, which is what some
	// non-issue-scoped sessions (admin smoke) want.
	if spec.WorkingBranch != "" && spec.WorkingBranch != spec.BaseBranch {
		// origin/<working> may or may not exist; rev-parse tells us
		// without an extra HTTP fetch (clone already populated remote
		// refs).
		hasRemote := runGit(ctx, spec.Dest, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+spec.WorkingBranch) == nil
		var checkoutArgs []string
		if hasRemote {
			checkoutArgs = []string{"checkout", "-B", spec.WorkingBranch, "refs/remotes/origin/" + spec.WorkingBranch}
		} else {
			// Branch from BaseBranch (= current HEAD post-clone).
			checkoutArgs = []string{"checkout", "-B", spec.WorkingBranch}
		}
		if err := runGit(ctx, spec.Dest, checkoutArgs...); err != nil {
			return "", fmt.Errorf("checkout %s: %w", spec.WorkingBranch, err)
		}
	}
	return spec.Dest, nil
}

// runGit invokes the git CLI with cwd set to dir (or inherited when
// blank), wiring stderr into the returned error so failures surface
// the actual git diagnostic instead of just "exit status 128".
func runGit(ctx context.Context, dir string, args ...string) error {
	return runGitWithEnv(ctx, dir, nil, args...)
}

// runGitWithEnv is runGit plus extra env entries (e.g. the session
// token the inline credential helper reads). The base env still
// inherits PATH from the runner process and suppresses host-side
// credential prompts so a misconfigured runner can't hang on stdin.
func runGitWithEnv(ctx context.Context, dir string, extraEnv []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/true",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Truncate noisy output so a 500-line clone log doesn't
		// blow up the runner's stderr.
		msg := strings.TrimSpace(string(out))
		if len(msg) > 512 {
			msg = msg[:512] + "…"
		}
		return fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, msg)
	}
	return nil
}

func branchOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
