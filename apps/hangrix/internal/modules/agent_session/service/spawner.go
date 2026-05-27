package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/bmatcuk/doublestar/v4"

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

// hostConfigPath is the canonical relative path inside a host repo. The
// agent_session spec pins this — a host yaml lookup never follows a
// redirect / alternative location.
const hostConfigPath = ".hangrix/agents.yml"

// Spawner is the agent_session orchestrator. Composition is deliberately
// wide — it touches the repo store, the resolver, the bare repo on disk,
// and the runner module's persistence — because the orchestrator sits
// exactly at the seam between "issue lifecycle event" and "session row +
// history frame". Splitting it further produces shells with one method
// each.
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
// the full fan-out / idempotency / live-session-enqueue contract.
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
	//   existingByRole  — role's most-recent session row (any status,
	//                     including archived). Used to branch the
	//                     wake-up: live (pending/claimed/running) →
	//                     enqueue onto its inputs; idle/failed/
	//                     succeeded/cancelled → rewake the row; archived
	//                     → spawn a fresh row that replaces the archived
	//                     predecessor (the archived row stays on disk
	//   alreadyForCause — (role, cause_kind, cause_id) tuple was
	//                     already processed by ANY non-archived session.
	//                     Re-firing the same cause onto ANY row that
	//                     still exists (live or terminal-but-not-archived)
	//                     is a no-op; this prevents duplicate inputs when
	//                     a previous rewake partially failed.
	existing, err := s.runner.ListSessionsByIssue(ctx, in.RepoID, in.IssueNumber)
	if err != nil {
		return nil, fmt.Errorf("spawner: list existing sessions: %w", err)
	}
	existingByRole := map[string]*runnerdomain.AgentSession{}
	alreadyForCause := map[string]struct{}{}
	for _, e := range existing {
		if e.RoleKey == "" {
			continue
		}
		// Most-recent row wins — ListSessionsByIssue returns rows in
		// spawn order, so a later overwrite keeps the freshest row as
		// the canonical one for the role.
		existingByRole[e.RoleKey] = e
		if e.Status != runnerdomain.SessionStatusArchived {
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
		// Direct-invoke callers (e.g. questionnaire submission) target
		// exactly one role semantically — bypass the per-role trigger
		// map so the role wakes even without an explicit trigger entry
		// in agents.yml.
		if !s.isDirectInvoke(in) && !triggerMatches(role.Triggers, in.Trigger, roleKey, in) {
			continue
		}
		// Idempotency: exact (role, cause_kind, cause_id) was already
		// processed. The TestOnTriggerIdempotent suite relies on this
		// for the issue.opened path where cause_id is "".
		if _, dup := alreadyForCause[causeKey(roleKey, string(in.CauseKind), in.CauseID)]; dup {
			continue
		}

		// failed sessions are not automatically rewoken — a failed
		// session may have orphaned inputs from a previous EnqueueInput-
		// succeeded-but-ResumeSession-failed path, and rewaking it with a
		// different cause would replay those orphaned inputs alongside the
		// new cause (cross-cause replay). A failed session needs explicit
		// operator recovery via controller.Resume/Recover.
		if existing, hasExisting := existingByRole[roleKey]; hasExisting &&
			existing.Status != runnerdomain.SessionStatusArchived &&
			existing.Status != runnerdomain.SessionStatusFailed {
			if isLiveStatus(existing.Status) {
				// Live row — agent is alive or about to be claimed.
				// Append the event onto its inputs queue; the agent
				// picks it up via its long-poll /inputs stream — no
				// fresh container spin-up.
				enq, err := s.enqueueOntoLive(ctx, in, existing)
				if err != nil {
					s.recordSpawnError(ctx, in, roleKey, err)
					continue
				}
				out = append(out, enq)
				continue
			}
			// Non-live, non-archived, non-failed (idle / succeeded /
			// cancelled). Rewake the row so the next runner poll
			// picks it up, and seed the new turn with the cause.
			// (failed sessions are excluded above — they may carry
			// orphaned inputs and need explicit operator recovery.)
			rew, err := s.rewakeRole(ctx, in, existing)
			if err != nil {
				s.recordSpawnError(ctx, in, roleKey, err)
				continue
			}
			out = append(out, rew)
			continue
		}

		spawn, err := s.spawnRole(ctx, in, hostRepo, hostCfg, role, roleKey, repoSHA)
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

// rewakeRole flips a non-live, non-archived session row back to
// 'pending' and enqueues a fresh history + cause frame so the agent
// can resume. The same session row is reused — per-issue per-role
// continuity is the spec's intent.
//
// Token policy: the session row identity (HANGRIX_SESSION_TOKEN) is
// preserved across rewake whenever the DB still has the sealed
// plaintext. Both MarkSessionIdle and MarkSessionTerminal now leave
// sealed intact, so the common path reads prefix / hash / sealed off
// the existing row and passes them through. The cloned .git/config
// uses an inline credential.helper that reads $HANGRIX_SESSION_TOKEN
// at request time, so the helper would actually tolerate a rotated
// token — but reusing the same row's token avoids DB churn and keeps
// audit trails coherent across an issue's full life.
//
// Legacy rows whose sealed was NULL'd by the old terminate path
// (rows that died before this change rolled out) fall back to a
// fresh mint: we have no plaintext to recover, so a new identity is
// the only option. Those rows still work — the new helper picks up
// the fresh value on next exec — and existing extraHeader-style
// clones from before the helper landed continue to authenticate
// because the same token is being re-installed on the row.
//
// The history frame is no longer seeded onto the inputs queue here.
// The runner fetches it from GET /sessions/{sid}/history at every agent
// process boot and writes it to stdin before draining /inputs. That
// keeps the agent's "first frame must be history" invariant intact even
// when status lags reality (crash mid-event, runner restart, container
// reuse) — paths where the previous enqueue-on-spawn design could leave
// a stale cause frame at the head of the queue.
func (s *Spawner) rewakeRole(ctx context.Context, in domain.TriggerInput, existing *runnerdomain.AgentSession) (domain.SpawnedSession, error) {
	tok, err := s.resumeToken(existing)
	if err != nil {
		return domain.SpawnedSession{}, err
	}

	// Enqueue the cause event and the audit message BEFORE flipping the
	// session status. If either of these DB writes fails, the session
	// stays idle — a retry on the next trigger is safe. The previous
	// ordering (resume first, then enqueue) could leave the session in
	// 'pending' with no input events if enqueue failed, causing the
	// runner to claim it, boot the agent, and the agent to wait forever
	// at 'ready' for an event that was never enqueued.
	frame, err := buildCauseFrame(in)
	if err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("build cause frame: %w", err)
	}
	if _, err := s.runner.EnqueueInput(ctx, existing.ID, frame); err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("enqueue cause on rewake: %w", err)
	}
	// Best-effort audit log before resume; a failure here is non-fatal
	// (the input is already enqueued, which is what drives the agent).
	_, _ = s.runner.AppendMessage(ctx, &runnerdomain.Message{
		SessionID: existing.ID,
		Kind:      runnerdomain.MessageKindEvent,
		EventName: string(in.Trigger),
		Payload:   frame,
	})

	// Only now flip the session to 'pending' — the runner will claim it
	// and the agent will find the event already waiting on /inputs.
	if err := s.runner.ResumeSession(ctx, existing.ID, tok); err != nil {
		// Best-effort: mark the session failed so a retry with the same
		// cause won't enqueue a duplicate input (alreadyForCause now
		// covers all non-archived sessions, not just live ones). The
		// orphaned input in the queue is residual — no DeleteInput API
		// exists — but the same-cause dedupe prevents accumulation.
		_ = s.runner.MarkSessionTerminal(ctx, existing.ID, runnerdomain.SessionStatusFailed, nil, "rewake resume failed: "+err.Error())
		return domain.SpawnedSession{}, fmt.Errorf("resume session: %w", err)
	}

	return domain.SpawnedSession{
		SessionID: existing.ID,
		RoleKey:   existing.RoleKey,
		RunnerID:  existing.RunnerID,
		Action:    domain.SpawnActionRewoken,
	}, nil
}

// resumeToken picks the token to install on a rewoken row. When the
// existing row still has its sealed plaintext (every fresh row plus
// any row that went through MarkSessionIdle / MarkSessionTerminal
// since the sealed-preservation change) we pass that identity through
// unchanged so the previous container's `.git/config` stays valid.
// Only legacy rows whose sealed was NULL'd under the old terminate
// behaviour fall through to minting a new token — accepted for the
// migration window, never reachable for newly-created sessions.
func (s *Spawner) resumeToken(existing *runnerdomain.AgentSession) (runnerdomain.NewSessionToken, error) {
	if existing.SessionTokenSealed != "" {
		return runnerdomain.NewSessionToken{
			Prefix: existing.SessionTokenPrefix,
			Hash:   existing.SessionTokenHash,
			Sealed: existing.SessionTokenSealed,
		}, nil
	}
	plaintext, prefix, hashed, err := service.MintSessionToken()
	if err != nil {
		return runnerdomain.NewSessionToken{}, fmt.Errorf("mint session token: %w", err)
	}
	sealed, err := s.box.Encrypt(plaintext)
	if err != nil {
		return runnerdomain.NewSessionToken{}, fmt.Errorf("seal session token: %w", err)
	}
	return runnerdomain.NewSessionToken{
		Prefix: prefix,
		Hash:   string(hashed),
		Sealed: sealed,
	}, nil
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
) (domain.SpawnedSession, error) {
	// Resolve the docker image tag. Either container.image (pre-built,
	// pulled by the runner) or container.build (a Dockerfile inside the
	// host repo that the runner builds on demand). The parser guarantees
	// exactly one is set; this just dispatches.
	image, err := agentsconfig.ResolveImageTag(hostRepo.ID, hostCfg.Container)
	if err != nil {
		return domain.SpawnedSession{}, err
	}

	// Resolve effective LLM: per-field merge of host.LLM (team) and
	// role.LLM (override). Empty model is rejected so the runner
	// doesn't ship an unparseable env.
	effective := resolveLLM(role, hostCfg)
	model := ""
	if effective != nil {
		model = effective.Model
	}
	if model == "" {
		return domain.SpawnedSession{}, fmt.Errorf("no llm model resolved (role + host both empty)")
	}

	// host_addendum: the role's prompt is the Markdown body of its
	// `.hangrix/agents/<role>.md` file, already loaded and frozen into
	// role.Prompt by LoadHostConfig.
	addendum := role.Prompt

	// Snapshot the resolved role config — host_addendum (after file
	// resolution), can list, scope, llm, container — so the audit row
	// can reproduce exactly what the agent saw without re-parsing host
	// yaml at the snapshot sha.
	snapshot, err := buildRoleSnapshot(role, hostCfg, addendum, model, effective, image)
	if err != nil {
		return domain.SpawnedSession{}, err
	}

	// Pick a runner. Default policy (PickAny) returns nil/nil — leaves
	// runner_id unset so ClaimNextSession on any eligible runner picks
	// the row up. Later milestones can install a smarter picker
	// without changing the spawner.
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
	// tool can `git commit` with the canonical role-key author. The
	// HANGRIX_SESSION_TOKEN family lives on the runner side — duplicating
	// them here would risk drift.
	identity := domain.IdentityForRole(roleKey, s.hostURL)
	env := map[string]string{
		"GIT_AUTHOR_NAME":     identity.Name,
		"GIT_AUTHOR_EMAIL":    identity.Email,
		"GIT_COMMITTER_NAME":  identity.Name,
		"GIT_COMMITTER_EMAIL": identity.Email,
		"HANGRIX_ROLE_KEY":    roleKey,
		"HANGRIX_REPO_SHA":    repoSHA,
		"HANGRIX_CAUSE_KIND":  string(in.CauseKind),
		"HANGRIX_CAUSE_ID":    in.CauseID,
		"HANGRIX_HOST_OWNER":  hostRepo.OwnerName,
		"HANGRIX_HOST_NAME":   hostRepo.Name,
		"HANGRIX_HOST_REPO":   hostRepo.OwnerName + "/" + hostRepo.Name,
	}
	// Host yaml env merges on top of the identity keys. A role yaml that
	// sets GIT_AUTHOR_NAME wins — though the spec doesn't carve that out
	// as a user-controllable knob, we honour explicit overrides for
	// debugging without a code change.
	for k, v := range hostCfg.Container.Env {
		env[k] = v
	}

	// Tool-access policy injected last so it wins over any host
	// container.env override. HANGRIX_REPO_PERMISSION is the coarse
	// read/write level the agent uses to hide write platform tools when
	// read-only; HANGRIX_PLATFORM_TOOLS is the role's resolved platform-
	// tool whitelist (glob patterns) the agent matches against its
	// platform tool registry, hiding everything else from the LLM schema.
	// The server still enforces read/write independently — these are the
	// agent-side schema-shaping inputs only.
	env["HANGRIX_REPO_PERMISSION"] = role.Permission
	if patternsJSON, err := json.Marshal(role.ToolPatterns); err == nil {
		env["HANGRIX_PLATFORM_TOOLS"] = string(patternsJSON)
	}

	createIn := runnerdomain.CreateSessionInput{
		RunnerID:           runnerID,
		RepoID:             &hostRepo.ID,
		IssueNumber:        &in.IssueNumber,
		Role:               roleKey,
		Model:              model,
		AgentImage:         image,
		WorkingBranch:      issueBranchName(in.IssueNumber),
		BaseBranch:         hostRepo.DefaultBranch,
		HostAddendum:       addendum,
		Env:                env,
		SessionTokenPrefix: prefix,
		SessionTokenHash:   string(hashed),
		SessionTokenSealed: sealed,
		CreatedBy:          in.ActorID,
		RepoSHA:            repoSHA,
		RoleKey:            roleKey,
		CauseKind:          string(in.CauseKind),
		CauseID:            in.CauseID,
		RoleConfig:         snapshot,
	}
	// Build the cause frame before touching the DB — if payload
	// unmarshalling fails, nothing has changed and the caller can
	// retry safely. (Same guard used in rewakeRole.)
	causeFrame, err := buildCauseFrame(in)
	if err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("build cause frame: %w", err)
	}

	sess, err := s.runner.CreateSession(ctx, createIn)
	if err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("create session row: %w", err)
	}

	// Seed the inputs queue with just the cause event. The history frame
	// the agent's loop reads as its first inbound is fetched directly by
	// the runner from GET /sessions/{sid}/history at every agent process
	// boot — keeping it off the inputs queue means the agent's first-frame
	// invariant survives crash-and-respawn paths that previously left a
	// stale cause at the head of the queue.
	if _, err := s.runner.EnqueueInput(ctx, sess.ID, causeFrame); err != nil {
		return domain.SpawnedSession{}, fmt.Errorf("enqueue cause: %w", err)
	}
	// Persist the cause event onto the message log too so the audit
	// timeline reflects "what triggered this session".
	_, _ = s.runner.AppendMessage(ctx, &runnerdomain.Message{
		SessionID: sess.ID,
		Kind:      runnerdomain.MessageKindEvent,
		EventName: string(in.Trigger),
		Payload:   causeFrame,
	})

	return domain.SpawnedSession{
		SessionID: sess.ID,
		RoleKey:   roleKey,
		RunnerID:  runnerID,
		Action:    domain.SpawnActionSpawned,
	}, nil
}

// hostFileProvider adapts HostBlobReader to agentsconfig.FileProvider for
// a fixed (repo fs, ref). The context is bound so the FileProvider
// interface can stay ctx-free; the adapter is short-lived (one load).
type hostFileProvider struct {
	ctx    context.Context
	blob   domain.HostBlobReader
	hostFs string
	ref    string
}

func (p *hostFileProvider) ReadFile(path string) ([]byte, bool) {
	return p.blob.ReadBlob(p.ctx, p.hostFs, p.ref, path)
}

func (p *hostFileProvider) ListDir(dir string) ([]string, bool) {
	return p.blob.ListBlobs(p.ctx, p.hostFs, p.ref, dir)
}

// loadHostConfig reads `.hangrix/agents.yml` (team config + tool rules)
// plus every `.hangrix/agents/<role>.md` role file from the base-branch
// tip and assembles them. Returns (nil, nil) when agents.yml is absent
// (non-agent host); (nil, ErrHostConfigInvalid) on parse/validation
// failure so callers can log and skip rather than re-derive the error.
func (s *Spawner) loadHostConfig(ctx context.Context, hostFs, branch string) (*agentsconfig.HostConfig, error) {
	fp := &hostFileProvider{ctx: ctx, blob: s.blob, hostFs: hostFs, ref: branch}
	cfg, err := agentsconfig.LoadHostConfig(fp)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrHostConfigInvalid, err)
	}
	if cfg == nil {
		return nil, nil
	}
	// NormalizeHostConfig is currently a no-op; we still call it so
	// future schema-level defaults can land in one well-known place
	// without every consumer needing to be re-touched.
	agentsconfig.NormalizeHostConfig(cfg)
	return cfg, nil
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

// triggerMatches decides whether a role's TriggerSpec map should wake
// for the incoming event. Two gates apply:
//
//  1. Subscription: the role must declare an entry for `want` in its
//     triggers map; absence means the role doesn't care about this
//     event regardless of payload.
//  2. Filter: when the entry carries a filter block (Comment / Push),
//     the corresponding TriggerInput context must satisfy every set
//     filter field (AND). Filter fields default to "no filter" — a
//     role with `issue.comment: {}` wakes on every comment.
//
// The roleKey is needed to evaluate mentioned_only (which checks the
// role's own key against the comment's mentions list).
func triggerMatches(triggers map[agentsconfig.Trigger]*agentsconfig.TriggerSpec, want agentsconfig.Trigger, roleKey string, in domain.TriggerInput) bool {
	spec, ok := triggers[want]
	if !ok {
		return false
	}
	if spec == nil {
		return true
	}
	if spec.Comment != nil {
		return evalCommentFilter(spec.Comment, roleKey, in.Comment)
	}
	if spec.Push != nil {
		return evalPushFilter(spec.Push, in.ChangedPaths)
	}
	return true
}

// isDirectInvoke reports whether the caller pinpointed exactly one role
// with a CauseKind that carries its own semantic targeting (e.g. a
// questionnaire submission → questionnaire author). When true the
// per-role triggers-map filter is bypassed — the role wakes regardless
// of its agents.yml trigger config. Each new CauseKind that should skip
// triggerMatches is added here, keeping a single gate for the direct-
// invoke path.
func (s *Spawner) isDirectInvoke(in domain.TriggerInput) bool {
	return in.RoleKey != "" && in.CauseKind == domain.CauseKindQuestionnaireAnswered
}

// evalCommentFilter returns true when the comment context satisfies
// every set field on f. Defensive against nil ctx: if the issue
// handler somehow fires issue.comment without a context, only the
// no-filter case fires (matches the pre-refactor "comment.any"
// behaviour for a role with `issue.comment: {}`).
func evalCommentFilter(f *agentsconfig.CommentFilter, roleKey string, ctx *domain.CommentContext) bool {
	noFilter := !f.MentionedOnly && len(f.FromRoles) == 0 && len(f.FromUsers) == 0
	if ctx == nil {
		return noFilter
	}
	if f.MentionedOnly {
		mentioned := false
		for _, m := range ctx.Mentions {
			if m == roleKey {
				mentioned = true
				break
			}
		}
		if !mentioned {
			return false
		}
	}
	if len(f.FromRoles) > 0 {
		if ctx.AuthorRoleKey == "" {
			return false
		}
		if !containsString(f.FromRoles, ctx.AuthorRoleKey) {
			return false
		}
	}
	if len(f.FromUsers) > 0 {
		if ctx.AuthorUser == "" {
			return false
		}
		if !containsString(f.FromUsers, ctx.AuthorUser) {
			return false
		}
	}
	return true
}

// evalPushFilter returns true when the changed-path list satisfies
// the include / ignore globs. Semantics mirror GitHub Actions:
//
//   - Paths (include): at least one changed file must match at least
//     one pattern. Empty Paths = no include gate.
//   - PathsIgnore: at least one changed file must NOT match every
//     ignore pattern (a push where every changed file is ignored is
//     skipped). Empty PathsIgnore = no ignore gate.
//
// An empty filter (both Paths and PathsIgnore unset) accepts every
// push. An empty ChangedPaths list accepts only the no-filter case —
// without files we cannot prove any path matches.
func evalPushFilter(f *agentsconfig.PushFilter, changed []string) bool {
	noFilter := len(f.Paths) == 0 && len(f.PathsIgnore) == 0
	if noFilter {
		return true
	}
	if len(changed) == 0 {
		return false
	}
	if len(f.Paths) > 0 {
		anyMatch := false
		for _, p := range changed {
			if anyGlobMatches(f.Paths, p) {
				anyMatch = true
				break
			}
		}
		if !anyMatch {
			return false
		}
	}
	if len(f.PathsIgnore) > 0 {
		anyKept := false
		for _, p := range changed {
			if !anyGlobMatches(f.PathsIgnore, p) {
				anyKept = true
				break
			}
		}
		if !anyKept {
			return false
		}
	}
	return true
}

// anyGlobMatches reports whether path matches at least one pattern.
// Uses doublestar for `**` support; on malformed patterns the host
// yaml validator should have caught the issue, but we treat parse
// errors here as "no match" rather than panicking the spawn loop.
func anyGlobMatches(patterns []string, path string) bool {
	for _, pat := range patterns {
		ok, err := doublestar.PathMatch(pat, path)
		if err == nil && ok {
			return true
		}
	}
	return false
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
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
// open-ended (a JSON object) so it can extend without a migration.
//
// effective is the per-field merged LLMConfig computed by resolveLLM in
// the caller. Pointer fields are dereferenced into the JSON (with
// ,omitempty + zero on nil) so the audit row carries the resolved
// scalar values an operator expects to see, without re-running the
// inheritance to read it back.
//
// resolvedImage is the tag the runner will use for `docker create` —
// either host.Container.Image verbatim or the deterministic build tag
// from resolveImageTag. The original host.Container.{Image,Build}
// spec is mirrored verbatim into the snapshot's `container` map so
// audit consumers can tell whether the runner pulled or built.
func buildRoleSnapshot(role *agentsconfig.Role, host *agentsconfig.HostConfig, addendum, model string, effective *agentsconfig.LLMConfig, resolvedImage string) ([]byte, error) {
	type rs struct {
		Triggers            map[string]any `json:"triggers"`
		Permission          string         `json:"permission"`
		ToolPatterns        []string       `json:"tool_patterns,omitempty"`
		ScopePaths          []string       `json:"scope_paths,omitempty"`
		HostAddendum        string         `json:"host_addendum,omitempty"`
		Model               string         `json:"model"`
		LLMMaxOutputTokens  int            `json:"llm_max_output_tokens,omitempty"`
		LLMMaxContextTokens int            `json:"llm_max_context_tokens,omitempty"`
		LLMReasoningEffort  string         `json:"llm_reasoning_effort,omitempty"`
		McpServers          []string       `json:"mcp_servers,omitempty"`
		Container           map[string]any `json:"container"`
	}
	snap := rs{
		Triggers:     serializeTriggers(role.Triggers),
		Permission:   role.Permission,
		ToolPatterns: append([]string(nil), role.ToolPatterns...),
		ScopePaths:   append([]string(nil), role.Scope.Paths...),
		HostAddendum: addendum,
		Model:        model,
		McpServers:   append([]string(nil), role.MCP...),
	}
	if effective != nil {
		if effective.MaxOutputTokens != nil {
			snap.LLMMaxOutputTokens = *effective.MaxOutputTokens
		}
		if effective.MaxContextTokens != nil {
			snap.LLMMaxContextTokens = *effective.MaxContextTokens
		}
		if effective.ReasoningEffort != nil {
			snap.LLMReasoningEffort = *effective.ReasoningEffort
		}
	}
	snap.Container = map[string]any{
		"image": host.Container.Image,
	}
	if host.Container.Build != nil {
		build := map[string]any{
			"dockerfile": host.Container.Build.Dockerfile,
		}
		if host.Container.Build.Context != "" {
			build["context"] = host.Container.Build.Context
		}
		if len(host.Container.Build.Args) > 0 {
			build["args"] = host.Container.Build.Args
		}
		snap.Container["build"] = build
	}
	if resolvedImage != "" && resolvedImage != host.Container.Image {
		// The runner needs the actual tag it should `docker create`
		// against; when build is set, this is the deterministic tag
		// the runner reproduces locally via `docker build -t <tag>`.
		snap.Container["resolved_image"] = resolvedImage
	}
	if len(host.Container.Entrypoint) > 0 {
		snap.Container["entrypoint"] = host.Container.Entrypoint
	}
	if len(host.Container.Volumes) > 0 {
		vols := make([]map[string]string, len(host.Container.Volumes))
		for i, v := range host.Container.Volumes {
			vols[i] = map[string]string{"name": v.Name, "mount": v.Mount}
		}
		snap.Container["volumes"] = vols
	}
	return json.Marshal(snap)
}


// serializeTriggers turns a role's Triggers map into an audit-stable
// JSON shape: `{ "<event>": <filter-or-empty-object> }`. The filter
// object echoes the per-event filter fields so an audit row can
// reconstruct the wakeup criteria without re-parsing host yaml.
func serializeTriggers(triggers map[agentsconfig.Trigger]*agentsconfig.TriggerSpec) map[string]any {
	out := make(map[string]any, len(triggers))
	for t, spec := range triggers {
		out[string(t)] = serializeTriggerSpec(spec)
	}
	return out
}

func serializeTriggerSpec(spec *agentsconfig.TriggerSpec) map[string]any {
	body := map[string]any{}
	if spec == nil {
		return body
	}
	if spec.Comment != nil {
		if spec.Comment.MentionedOnly {
			body["mentioned_only"] = true
		}
		if len(spec.Comment.FromRoles) > 0 {
			body["from_roles"] = spec.Comment.FromRoles
		}
		if len(spec.Comment.FromUsers) > 0 {
			body["from_users"] = spec.Comment.FromUsers
		}
	}
	if spec.Push != nil {
		if len(spec.Push.Paths) > 0 {
			body["paths"] = spec.Push.Paths
		}
		if len(spec.Push.PathsIgnore) > 0 {
			body["paths_ignore"] = spec.Push.PathsIgnore
		}
	}
	return body
}

// resolveLLM returns the effective LLM block for a role by merging the
// team default (host.LLM) and the per-role override (role.LLM)
// field-by-field. A non-nil pointer on role.LLM wins; a nil pointer
// inherits the team's value. Returns nil only when both are nil.
//
// Model is a string (not a pointer) so the inheritance test is "role
// non-empty" → role wins, else team. Every other scalar uses pointer
// non-nil to mean "explicitly set" — that's how a role can override
// `temperature: 0` or `reasoning_effort: ""` without it being
// indistinguishable from "field omitted".
func resolveLLM(role *agentsconfig.Role, host *agentsconfig.HostConfig) *agentsconfig.LLMConfig {
	var team, perRole *agentsconfig.LLMConfig
	if host != nil {
		team = host.LLM
	}
	if role != nil {
		perRole = role.LLM
	}
	if team == nil && perRole == nil {
		return nil
	}
	out := &agentsconfig.LLMConfig{}
	if team != nil {
		*out = *team
	}
	if perRole != nil {
		if perRole.Model != "" {
			out.Model = perRole.Model
		}
		if perRole.MaxOutputTokens != nil {
			out.MaxOutputTokens = perRole.MaxOutputTokens
		}
		if perRole.MaxContextTokens != nil {
			out.MaxContextTokens = perRole.MaxContextTokens
		}
		if perRole.ReasoningEffort != nil {
			out.ReasoningEffort = perRole.ReasoningEffort
		}
		if perRole.Temperature != nil {
			out.Temperature = perRole.Temperature
		}
		if perRole.TopP != nil {
			out.TopP = perRole.TopP
		}
	}
	return out
}

// buildCauseFrame is the JSON the runner writes to agent stdin to tell
// the agent what woke it. The shape mirrors the kind=event frame the
// admin smoke path emits, so the agent's IPC parser doesn't branch.
// Per-trigger details (comment body, push delta, vote value) ride
// in.Payload — the spawner merges them into the payload object without
// rewriting the wire shape.
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
