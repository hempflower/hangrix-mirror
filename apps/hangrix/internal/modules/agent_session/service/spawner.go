package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/service"
	"github.com/hangrix/hangrix/pkg/cryptobox"
)

// hostConfigPath / hostLockPath are the canonical relative paths inside a
// host repo. The agent_session spec pins these — a host yaml lookup never
// follows a redirect / alternative location.
const (
	hostConfigPath = ".hangrix/agents.yml"
	hostLockPath   = ".hangrix/agents.lock"
)

// Spawner is the agent_session orchestrator. Composition is deliberately
// wide — it touches the repo store, the resolver, the bare repo on disk,
// and the runner module's persistence — because M7a P2 sits exactly at
// the seam between "issue lifecycle event" and "session row + history
// frame". Splitting it further produces shells with one method each.
type Spawner struct {
	repos    repodomain.Store
	resolver orgdomain.Resolver
	storage  repodomain.PathResolver
	git      gitdomain.Git
	blob     domain.HostBlobReader
	runner   runnerdomain.Repo
	box      *cryptobox.Box
	hostURL  string
}

type SpawnerDeps struct {
	Repos    repodomain.Store
	Resolver orgdomain.Resolver
	Storage  repodomain.PathResolver
	Git      gitdomain.Git
	Blob     domain.HostBlobReader
	Runner   runnerdomain.Repo
	Config   *config.Config
}

// NewSpawner panics on a malformed encryption key — the runner module
// uses the same key and would have panicked at startup too. That keeps
// the failure mode consistent for an operator.
func NewSpawner(deps *SpawnerDeps) *Spawner {
	box, err := cryptobox.New(deps.Config.LLM.EncryptionKey)
	if err != nil {
		panic(fmt.Errorf("agent_session spawner: %w", err))
	}
	return &Spawner{
		repos:    deps.Repos,
		resolver: deps.Resolver,
		storage:  deps.Storage,
		git:      deps.Git,
		blob:     deps.Blob,
		runner:   deps.Runner,
		box:      box,
		hostURL:  deps.Config.Server.URL,
	}
}

// OnTrigger satisfies domain.Spawner. See the interface docstring for
// the full fan-out / idempotency / live-session-enqueue contract; this
// implementation is the M7b orchestrator.
//
// Wide method — most of the spawn flow lives here — because the steps
// share a lot of resolved state (host repo, fs path, base-branch sha,
// host config) that would otherwise need to be plumbed through helper
// signatures.
//
// Per-role failures (missing agent repo, unresolved ref, etc.) are
// logged-via-error and skipped; other roles continue. A whole-config
// failure (missing or invalid host yaml) returns the sentinel so the
// issue handler can decide whether to log + continue or surface to the
// user.
func (s *Spawner) OnTrigger(ctx context.Context, in domain.TriggerInput) ([]domain.SpawnedSession, error) {
	if in.RepoID <= 0 || in.IssueNumber <= 0 {
		return nil, fmt.Errorf("spawner: repo_id and issue_number required")
	}
	if !agentsconfig.IsValidTrigger(string(in.Trigger)) {
		return nil, fmt.Errorf("spawner: trigger %q not recognised", in.Trigger)
	}
	log.Printf("spawner: OnTrigger repo=%d issue=%d trigger=%s cause=%s role=%q",
		in.RepoID, in.IssueNumber, in.Trigger, in.CauseID, in.RoleKey)

	hostRepo, err := s.repos.GetByID(ctx, in.RepoID)
	if err != nil {
		return nil, fmt.Errorf("spawner: host repo lookup: %w", err)
	}
	if hostRepo.DefaultBranch == "" {
		// Repo has no commits yet — nothing to spawn against.
		return nil, nil
	}
	hostFs, err := s.storage.ResolvePath(hostRepo.OwnerName, hostRepo.Name)
	if err != nil {
		return nil, fmt.Errorf("spawner: resolve host fs path: %w", err)
	}

	hostCfg, err := s.loadHostConfig(ctx, hostFs, hostRepo.DefaultBranch)
	if err != nil {
		return nil, err
	}
	if hostCfg == nil {
		// Non-agent host — no roles to spawn. Not an error; this is the
		// common case for repos that never opted into agent collaboration.
		return nil, nil
	}

	lock, _ := s.loadLockFile(ctx, hostFs, hostRepo.DefaultBranch)

	repoSHA, err := s.git.ResolveCommit(hostFs, hostRepo.DefaultBranch)
	if err != nil {
		return nil, fmt.Errorf("spawner: resolve repo_sha: %w", err)
	}
	if repoSHA == "" {
		// Unborn default branch — same outcome as missing host yaml.
		return nil, nil
	}

	// Index existing sessions for the (repo, issue):
	//
	//   archivedRoles   — role had a previous session that's now
	//                     archived. Spec says archive is terminal,
	//                     so the role is silenced for this issue.
	//   liveByRole      — role has a non-terminal session
	//                     (pending/claimed/running). The trigger
	//                     event becomes an input frame on this row.
	//   alreadyForCause — (role, cause_kind, cause_id) tuple was
	//                     already processed. Re-firing returns no-op.
	existing, err := s.runner.ListSessionsByIssue(ctx, in.RepoID, in.IssueNumber)
	if err != nil {
		return nil, fmt.Errorf("spawner: list existing sessions: %w", err)
	}
	archivedRoles := map[string]struct{}{}
	liveByRole := map[string]*runnerdomain.AgentSession{}
	alreadyForCause := map[string]struct{}{}
	for _, e := range existing {
		if e.RoleKey == "" {
			continue
		}
		if e.Status == runnerdomain.SessionStatusArchived {
			archivedRoles[e.RoleKey] = struct{}{}
			continue
		}
		// Only LIVE (pending/claimed/running) rows make a (role,
		// cause) tuple "already processed". Terminal-but-not-archived
		// rows (succeeded/failed/cancelled/idle) describe a previous
		// container — re-firing the same cause spawns a fresh row so
		// retries / multi-turn scenarios work.
		if isLiveStatus(e.Status) {
			liveByRole[e.RoleKey] = e
			alreadyForCause[causeKey(e.RoleKey, e.CauseKind, e.CauseID)] = struct{}{}
		}
	}

	out := make([]domain.SpawnedSession, 0, len(hostCfg.Roles))

	// hostCfg.Roles iteration is map-order-undefined per agentsconfig
	// docstring; sort for deterministic spawn order so audit consumers
	// see role rows in a stable order.
	keys := sortedRoleKeys(hostCfg.Roles)
	for _, roleKey := range keys {
		// RoleKey scoping (mention path). Empty matches all.
		if in.RoleKey != "" && roleKey != in.RoleKey {
			continue
		}
		role := hostCfg.Roles[roleKey]
		if !triggerMatches(role.Triggers, in.Trigger) {
			continue
		}
		if _, dead := archivedRoles[roleKey]; dead {
			continue
		}
		// Idempotency: exact (role, cause_kind, cause_id) was already
		// processed. M7a's TestOnTriggerIdempotent relies on this for
		// the issue.opened path where cause_id is "".
		if _, dup := alreadyForCause[causeKey(roleKey, string(in.CauseKind), in.CauseID)]; dup {
			continue
		}

		if live, hasLive := liveByRole[roleKey]; hasLive {
			// Append the event onto the running session's inputs queue
			// instead of creating a new row. The agent picks it up via
			// its long-poll /inputs stream — no fresh container spin-up.
			enq, err := s.enqueueOntoLive(ctx, in, live)
			if err != nil {
				s.recordSpawnError(ctx, in, roleKey, err)
				continue
			}
			out = append(out, enq)
			continue
		}

		spawn, err := s.spawnRole(ctx, in, hostRepo, hostCfg, role, roleKey, repoSHA, lock)
		if err != nil {
			// Per-role failure — log and continue with the next role.
			// We surface the error string on a kind=system message so a
			// later audit (or admin operator) can see why the role
			// didn't wake.
			s.recordSpawnError(ctx, in, roleKey, err)
			continue
		}
		out = append(out, spawn)
	}
	return out, nil
}

// LoadHostConfig satisfies domain.Spawner. It re-resolves the host yaml at
// the current default-branch tip every call so callers see the same view
// the next OnTrigger would. The host yaml is small (kilobytes) and the
// underlying read is `git cat-file -p`; memoising would add invalidation
// complexity for a negligible win.
//
// Returns (nil, nil) when the host repo has no `.hangrix/agents.yml`.
// Parse failures bubble up wrapped in ErrHostConfigInvalid so callers can
// distinguish "non-agent host" from "agent host with a broken file".
func (s *Spawner) LoadHostConfig(ctx context.Context, repoID int64) (*agentsconfig.HostConfig, error) {
	hostRepo, err := s.repos.GetByID(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("spawner: host repo lookup: %w", err)
	}
	if hostRepo.DefaultBranch == "" {
		return nil, nil
	}
	hostFs, err := s.storage.ResolvePath(hostRepo.OwnerName, hostRepo.Name)
	if err != nil {
		return nil, fmt.Errorf("spawner: resolve host fs path: %w", err)
	}
	return s.loadHostConfig(ctx, hostFs, hostRepo.DefaultBranch)
}

// enqueueOntoLive appends a cause-event input frame onto an existing
// running session's inputs queue. The session row itself is not mutated
// — the agent reads from its long-poll /inputs stream and acts on the
// new event without a fresh container spin-up. We also mirror the event
// into the message log so audit replay reflects "this event reached the
// session" even before the agent emits any response.
func (s *Spawner) enqueueOntoLive(ctx context.Context, in domain.TriggerInput, live *runnerdomain.AgentSession) (domain.SpawnedSession, error) {
	frame, err := buildCauseFrame(in)
	if err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("build cause frame: %w", err)
	}
	if _, err := s.runner.EnqueueInput(ctx, live.ID, frame); err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("enqueue input: %w", err)
	}
	_, _ = s.runner.AppendMessage(ctx, &runnerdomain.Message{
		SessionID: live.ID,
		Kind:      runnerdomain.MessageKindEvent,
		EventName: string(in.Trigger),
		Payload:   frame,
	})
	return domain.SpawnedSession{
		SessionID: live.ID,
		RoleKey:   live.RoleKey,
		AgentRepo: live.AgentRepo,
		RunnerID:  live.RunnerID,
		Action:    domain.SpawnActionEnqueued,
	}, nil
}

// causeKey builds the (role, cause_kind, cause_id) tuple used to dedup
// re-fires of the exact same upstream event. The literal `|` separator
// is safe — role keys are `^[a-z][a-z0-9-]*$`, cause kinds are short
// slugs, and cause IDs come from us (comment id, sha) so no value
// contains a pipe.
func causeKey(role, kind, id string) string {
	return role + "|" + kind + "|" + id
}

// isLiveStatus returns true for statuses where the session row is still
// holding a runnable container or claimable pending slot. terminal-but-
// not-archived statuses (succeeded/failed/cancelled/idle) return false:
// the container is gone, the sealed token is NULL'd, and a new turn
// requires a fresh row.
func isLiveStatus(status runnerdomain.SessionStatus) bool {
	switch status {
	case runnerdomain.SessionStatusPending,
		runnerdomain.SessionStatusClaimed,
		runnerdomain.SessionStatusRunning:
		return true
	}
	return false
}

// spawnRole is the per-role half of OnTrigger. Extracted so the outer
// loop stays readable; nothing else calls it.
func (s *Spawner) spawnRole(
	ctx context.Context,
	in domain.TriggerInput,
	hostRepo *repodomain.Repo,
	hostCfg *agentsconfig.HostConfig,
	role *agentsconfig.Role,
	roleKey string,
	repoSHA string,
	lock *agentsconfig.LockFile,
) (domain.SpawnedSession, error) {
	agentSHA, err := s.resolveAgentSHA(ctx, role.Agent, lock)
	if err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("resolve agent sha: %w", err)
	}
	agentRepo := role.Agent.Owner + "/" + role.Agent.Name + "@" + agentSHA

	// Container image: M7a only supports pre-built `container.image`.
	// `container.build` is parsed by agentsconfig but the runner doesn't
	// build images yet — fail loudly so an operator sees the gap.
	if hostCfg.Container.Image == "" {
		return domain.SpawnedSession{}, fmt.Errorf("host yaml: container.image is required (build: not yet supported)")
	}

	// Resolve effective LLM: role.LLM > host.LLM > {empty}. Empty model
	// is rejected so the runner doesn't ship an unparseable env.
	model := ""
	switch {
	case role.LLM != nil && role.LLM.Model != "":
		model = role.LLM.Model
	case hostCfg.LLM != nil && hostCfg.LLM.Model != "":
		model = hostCfg.LLM.Model
	}
	if model == "" {
		return domain.SpawnedSession{}, fmt.Errorf("no llm model resolved (role + host both empty)")
	}

	// host_addendum: role.Prompt is the inline string; PromptFile is a
	// path under .hangrix/prompts/ to load at session-spawn. We resolve
	// the file at spawn time so the snapshot is frozen.
	addendum, err := s.resolveHostAddendum(ctx, hostRepo, role)
	if err != nil {
		return domain.SpawnedSession{}, err
	}

	// Snapshot the resolved role config — host_addendum (after file
	// resolution), can list, scope, llm, container — so the audit row
	// can reproduce exactly what the agent saw without re-parsing host
	// yaml at the snapshot sha.
	snapshot, err := buildRoleSnapshot(role, hostCfg, addendum, model)
	if err != nil {
		return domain.SpawnedSession{}, err
	}

	// Pick a runner. Default policy (PickAny) returns nil/nil — leaves
	// runner_id unset so ClaimNextSession on any eligible runner picks
	// the row up. M7a accepts unpinned rows; later milestones can
	// install a smarter picker without changing the spawner.
	runnerID, err := s.pickRunner(ctx, hostRepo)
	if err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("pick runner: %w", err)
	}

	// Mint session identity. The session token is the in-container
	// agent's bearer on every platform call (LLM proxy, MCP). Plaintext
	// goes into the sealed column; the bcrypt(secret) into the hash
	// column. We don't echo plaintext back to the issue handler —
	// only the runner sees it at claim time.
	plaintext, prefix, hashed, err := service.MintSessionToken()
	if err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("mint session token: %w", err)
	}
	sealed, err := s.box.Encrypt(plaintext)
	if err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("seal session token: %w", err)
	}

	// Env: HANGRIX_* injected by the runner orchestrator at container
	// start; the spawner adds the role identity so the agent's `bash`
	// tool can `git commit` with the canonical role-key author. M6c env
	// keys (HANGRIX_SESSION_TOKEN, etc.) come from the runner side, not
	// here — duplicating them would risk drift.
	//
	// The HANGRIX_* snapshot keys (AGENT_SHA / REPO_SHA / CAUSE_KIND /
	// CAUSE_ID) make the row's audit pins visible to the in-container
	// agent for log emission. They duplicate what's already on the
	// agent_sessions row — the agent prefers the env because it would
	// otherwise need a platform-MCP call to learn its own snapshot.
	identity := domain.IdentityForRole(roleKey, s.hostURL)
	env := map[string]string{
		"GIT_AUTHOR_NAME":     identity.Name,
		"GIT_AUTHOR_EMAIL":    identity.Email,
		"GIT_COMMITTER_NAME":  identity.Name,
		"GIT_COMMITTER_EMAIL": identity.Email,
		"HANGRIX_ROLE_KEY":    roleKey,
		"HANGRIX_AGENT_REPO":  agentRepo,
		"HANGRIX_AGENT_SHA":   agentSHA,
		"HANGRIX_REPO_SHA":    repoSHA,
		"HANGRIX_CAUSE_KIND":  string(in.CauseKind),
		"HANGRIX_CAUSE_ID":    in.CauseID,
		// M7b: host repo coordinates the agent needs to clone / push.
		// Owner + name come from the host repo row; the runner already
		// forwards HANGRIX_WORKING_BRANCH / HANGRIX_BASE_BRANCH, and
		// HANGRIX_LLM_ENDPOINT carries the server base URL the agent
		// can strip down for git over HTTP.
		"HANGRIX_HOST_OWNER": hostRepo.OwnerName,
		"HANGRIX_HOST_NAME":  hostRepo.Name,
		"HANGRIX_HOST_REPO":  hostRepo.OwnerName + "/" + hostRepo.Name,
	}
	// Host yaml env merges on top of the identity keys. A role yaml that
	// sets GIT_AUTHOR_NAME wins — though the spec doesn't carve that out
	// as a user-controllable knob, we honour explicit overrides for
	// debugging without a code change.
	for k, v := range hostCfg.Container.Env {
		env[k] = v
	}

	createIn := runnerdomain.CreateSessionInput{
		RunnerID:           runnerID,
		RepoID:             &hostRepo.ID,
		IssueNumber:        &in.IssueNumber,
		Role:               roleKey,
		Model:              model,
		AgentImage:         hostCfg.Container.Image,
		AgentRepo:          agentRepo,
		WorkingBranch:      issueBranchName(in.IssueNumber),
		BaseBranch:         hostRepo.DefaultBranch,
		HostAddendum:       addendum,
		Env:                env,
		SessionTokenPrefix: prefix,
		SessionTokenHash:   string(hashed),
		SessionTokenSealed: sealed,
		CreatedBy:          in.ActorID,
		// M7a snapshot.
		AgentSHA:   agentSHA,
		RepoSHA:    repoSHA,
		RoleKey:    roleKey,
		CauseKind:  string(in.CauseKind),
		CauseID:    in.CauseID,
		RoleConfig: snapshot,
	}
	sess, err := s.runner.CreateSession(ctx, createIn)
	if err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("create session row: %w", err)
	}

	// Seed the inputs queue: history frame first (mirrors the M6c admin
	// path — agent stdin always opens with a history frame, empty until
	// M7b's event replay), then the cause event so the agent sees what
	// woke it.
	history := []byte(`{"kind":"history","messages":[]}`)
	if _, err := s.runner.EnqueueInput(ctx, sess.ID, history); err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("enqueue history: %w", err)
	}
	causeFrame, err := buildCauseFrame(in)
	if err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("build cause frame: %w", err)
	}
	if _, err := s.runner.EnqueueInput(ctx, sess.ID, causeFrame); err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("enqueue cause: %w", err)
	}
	// Persist the cause event onto the message log too so the audit
	// timeline reflects "what triggered this session" even before M7b's
	// event bus is wired.
	_, _ = s.runner.AppendMessage(ctx, &runnerdomain.Message{
		SessionID: sess.ID,
		Kind:      runnerdomain.MessageKindEvent,
		EventName: string(in.Trigger),
		Payload:   causeFrame,
	})

	return domain.SpawnedSession{
		SessionID: sess.ID,
		RoleKey:   roleKey,
		AgentRepo: agentRepo,
		RunnerID:  runnerID,
		Action:    domain.SpawnActionSpawned,
	}, nil
}

// loadHostConfig reads `.hangrix/agents.yml` from the base-branch tip and
// parses it. Returns (nil, nil) when the file is absent (non-agent host);
// (nil, ErrHostConfigInvalid) on parse failure so callers can log and
// skip rather than re-derive the error.
func (s *Spawner) loadHostConfig(ctx context.Context, hostFs, branch string) (*agentsconfig.HostConfig, error) {
	body, ok := s.blob.ReadBlob(ctx, hostFs, branch, hostConfigPath)
	if !ok {
		return nil, nil
	}
	cfg, err := agentsconfig.ParseHostConfig(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrHostConfigInvalid, err)
	}
	// Apply schema-level defaults (mention_by, etc.) so consumers don't
	// have to special-case the empty string. NormalizeHostConfig is
	// idempotent so calling it on every load is safe.
	agentsconfig.NormalizeHostConfig(cfg)
	return cfg, nil
}

// loadLockFile reads `.hangrix/agents.lock`. Missing or unparseable lock
// is non-fatal: the spawner falls back to live ResolveCommit on the agent
// repo. (Missing lock file is the common case for a freshly-installed
// agent until an operator commits the lock — failing here would block
// every spawn until that lock lands.)
func (s *Spawner) loadLockFile(ctx context.Context, hostFs, branch string) (*agentsconfig.LockFile, error) {
	body, ok := s.blob.ReadBlob(ctx, hostFs, branch, hostLockPath)
	if !ok {
		return nil, nil
	}
	return agentsconfig.ParseLockFile(body)
}

// resolveAgentSHA turns a role's `agent: <owner>/<name>@<ref>` into a
// commit sha. Lock file wins when it has an entry; otherwise we resolve
// against the agent repo's own refs via git.ResolveCommit. The agent
// repo must (a) exist and (b) be classified kind=agent — refusing to
// pull non-agent code into an agent container is a defensive check; the
// runner bundle endpoint enforces the same gate independently.
func (s *Spawner) resolveAgentSHA(ctx context.Context, ref agentsconfig.AgentRef, lock *agentsconfig.LockFile) (string, error) {
	if lock != nil {
		if entry, ok := lock.Agents[agentsconfig.LockKey(ref)]; ok && entry.ResolvedSHA != "" {
			return entry.ResolvedSHA, nil
		}
	}
	resolved, err := s.resolver.ResolveOwner(ctx, ref.Owner)
	if err != nil {
		return "", fmt.Errorf("%w: %s", domain.ErrAgentRepoNotFound, ref.Owner)
	}
	agentRepo, err := s.repos.GetByOwnerAndName(ctx, repodomain.OwnerKind(resolved.Kind), resolved.ID, ref.Name)
	if err != nil {
		return "", fmt.Errorf("%w: %s/%s", domain.ErrAgentRepoNotFound, ref.Owner, ref.Name)
	}
	if agentRepo.Kind != repodomain.KindAgent {
		return "", fmt.Errorf("%w: %s/%s is kind=%s", domain.ErrAgentRepoNotFound, ref.Owner, ref.Name, agentRepo.Kind)
	}
	agentFs, err := s.storage.ResolvePath(agentRepo.OwnerName, agentRepo.Name)
	if err != nil {
		return "", fmt.Errorf("resolve agent fs path: %w", err)
	}
	sha, err := s.git.ResolveCommit(agentFs, ref.Ref)
	if err != nil || sha == "" {
		return "", fmt.Errorf("%w: %s@%s", domain.ErrAgentRefUnresolved, ref.Name, ref.Ref)
	}
	return sha, nil
}

// resolveHostAddendum returns the host-side prompt addendum text. Inline
// `prompt:` wins when set; otherwise the file at `prompt_file:` is
// loaded from the base-branch tip and frozen into the snapshot. Empty
// string for roles with neither.
func (s *Spawner) resolveHostAddendum(ctx context.Context, hostRepo *repodomain.Repo, role *agentsconfig.Role) (string, error) {
	if role.Prompt != "" {
		return role.Prompt, nil
	}
	if role.PromptFile == "" {
		return "", nil
	}
	hostFs, err := s.storage.ResolvePath(hostRepo.OwnerName, hostRepo.Name)
	if err != nil {
		return "", fmt.Errorf("resolve host fs path for addendum: %w", err)
	}
	body, ok := s.blob.ReadBlob(ctx, hostFs, hostRepo.DefaultBranch, role.PromptFile)
	if !ok {
		// Missing prompt file is a config error in the host yaml; we
		// don't silently fall back to empty because that would erase a
		// behavioural contract.
		return "", fmt.Errorf("host yaml: prompt_file %q not found at %s", role.PromptFile, hostRepo.DefaultBranch)
	}
	return string(body), nil
}

// pickRunner returns nil on the default "any-runner" policy. Spec
// (docs/agent-config.md §"Session 模型") accepts unpinned rows; the
// next runner that polls /api/runner/tasks claims them. A later
// milestone can swap in a smarter picker without rewiring the spawner.
func (s *Spawner) pickRunner(ctx context.Context, hostRepo *repodomain.Repo) (*int64, error) {
	_ = ctx
	_ = hostRepo
	return nil, nil
}

// recordSpawnError logs the per-role spawn failure to stderr so an
// operator can see why a trigger didn't wake the expected role. We
// don't have a session row to attach the error to (CreateSession
// failed or wasn't reached); a proper event log is the M7c follow-up.
// For now stderr keeps the failure mode visible without needing the
// event bus.
func (s *Spawner) recordSpawnError(ctx context.Context, in domain.TriggerInput, roleKey string, err error) {
	_ = ctx
	log.Printf("spawner: skip role %q on trigger %s (repo=%d issue=%d cause=%s): %v",
		roleKey, in.Trigger, in.RepoID, in.IssueNumber, in.CauseID, err)
}

func triggerMatches(triggers []string, want agentsconfig.Trigger) bool {
	for _, t := range triggers {
		if t == string(want) {
			return true
		}
	}
	return false
}

func sortedRoleKeys(roles map[string]*agentsconfig.Role) []string {
	keys := make([]string, 0, len(roles))
	for k := range roles {
		keys = append(keys, k)
	}
	// In-place insertion sort: deterministic + tiny N (≤ a few dozen
	// roles).
	for i := 1; i < len(keys); i++ {
		j := i
		for j > 0 && keys[j-1] > keys[j] {
			keys[j-1], keys[j] = keys[j], keys[j-1]
			j--
		}
	}
	return keys
}

// issueBranchName mirrors the repo/handler convention: an issue's working
// branch is `issue/<n>`. Hard-coded here too because the issue module
// doesn't yet expose the name as a constant.
func issueBranchName(n int32) string {
	return fmt.Sprintf("issue/%d", n)
}

// buildRoleSnapshot freezes the resolved role config into the JSON blob
// stored on `agent_sessions.role_config`. Schema is intentionally
// open-ended (a JSON object) so M7b can extend without a migration.
func buildRoleSnapshot(role *agentsconfig.Role, host *agentsconfig.HostConfig, addendum, model string) ([]byte, error) {
	type rs struct {
		Triggers     []string        `json:"triggers"`
		Can          []string        `json:"can"`
		ScopePaths   []string        `json:"scope_paths,omitempty"`
		MentionBy    string          `json:"mention_by,omitempty"`
		HostAddendum string          `json:"host_addendum,omitempty"`
		Model        string          `json:"model"`
		LLMMaxTokens int             `json:"llm_max_tokens,omitempty"`
		Container    map[string]any  `json:"container"`
	}
	snap := rs{
		Triggers:     append([]string(nil), role.Triggers...),
		Can:          append([]string(nil), role.Can...),
		ScopePaths:   append([]string(nil), role.Scope.Paths...),
		MentionBy:    string(role.MentionBy),
		HostAddendum: addendum,
		Model:        model,
	}
	if role.LLM != nil {
		snap.LLMMaxTokens = role.LLM.MaxTokens
	}
	snap.Container = map[string]any{
		"image": host.Container.Image,
	}
	if len(host.Container.Secrets) > 0 {
		snap.Container["secrets"] = host.Container.Secrets
	}
	return json.Marshal(snap)
}

// buildCauseFrame is the JSON the runner writes to agent stdin to tell
// the agent what woke it. M6c admin used kind=event with a free-form
// payload; M7a follows the same shape so the agent's IPC parser doesn't
// branch. M7b layers per-trigger details (comment body, push delta,
// vote value) on top via in.Payload — the spawner merges them into the
// payload object without rewriting the wire shape.
func buildCauseFrame(in domain.TriggerInput) ([]byte, error) {
	payload := map[string]any{
		"repo_id":      in.RepoID,
		"issue_number": in.IssueNumber,
		"actor_id":     in.ActorID,
		"cause_kind":   string(in.CauseKind),
		"cause_id":     in.CauseID,
	}
	if len(in.Payload) > 0 {
		var extra map[string]any
		if err := json.Unmarshal(in.Payload, &extra); err != nil {
			return nil, fmt.Errorf("cause payload not a JSON object: %w", err)
		}
		// Caller-supplied keys win over the defaults. Callers don't
		// rebind repo_id / issue_number in practice; this guard is
		// just for completeness.
		for k, v := range extra {
			payload[k] = v
		}
	}
	frame := map[string]any{
		"kind":    "event",
		"event":   string(in.Trigger),
		"payload": payload,
	}
	return json.Marshal(frame)
}

// compile-time check
var _ domain.Spawner = (*Spawner)(nil)
