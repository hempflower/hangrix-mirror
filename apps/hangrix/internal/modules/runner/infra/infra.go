// Package infra holds the Postgres-backed implementation of the runner
// domain. The SQL surface lives in queries.sql; sqlc generates the
// typed accessors under runnerdb/. This file owns the (de)serialisation
// between generated row types and the domain model, plus the transaction
// glue for the multi-statement RedeemEnrollment + ClaimNextSession +
// ClaimPendingInputs flows.
//
// The on-disk shape of enrollment / agent / session tokens mirrors PATs:
// (public prefix, bcrypt(secret)) so revocation is one UPDATE and
// validation is O(1) by prefix.
package infra

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/infra/runnerdb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type PostgresRepo struct {
	q    *runnerdb.Queries
	pool *pgxpool.Pool
}

type PostgresRepoDeps struct {
	Pool *pgxpool.Pool
	// Repos forces the repo module's migrations to run before our own.
	// `agent_sessions.repo_id` is a FK on `repos(id)` — a fresh-DB
	// boot that constructs runner.PostgresRepo before repo.PostgresStore
	// would otherwise hit "relation repos does not exist" inside our
	// 00001_create_runners.sql. The dependency is purely an ordering
	// signal to ioc; we never call methods on it.
	Repos repodomain.Store
}

// NewPostgresRepo applies migrations up-front so a schema drift surfaces at
// startup, not on the first runner enrollment.
func NewPostgresRepo(deps *PostgresRepoDeps) *PostgresRepo {
	_ = deps.Repos // dependency-only — see PostgresRepoDeps docstring.
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("runner migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_runner", "."); err != nil {
		panic(fmt.Errorf("apply runner migrations: %w", err))
	}
	return &PostgresRepo{
		q:    runnerdb.New(deps.Pool),
		pool: deps.Pool,
	}
}

// ---- runners ----

// CreateRunner inserts a runner row with a pre-minted enrollment
// token. Visibility and owner-presence invariants must have been
// checked via domain.CreateRunnerInput.Validate() by the caller —
// Repo only writes; the DB CHECK constraint is the safety net.
func (r *PostgresRepo) CreateRunner(
	ctx context.Context,
	in domain.CreateRunnerInput,
	enroll domain.NewEnrollToken,
) (*domain.Runner, error) {
	var ownerArg pgtype.Int8
	if in.OwnerUserID != nil {
		ownerArg = pgtype.Int8{Int64: *in.OwnerUserID, Valid: true}
	}
	row, err := r.q.CreateRunner(ctx, runnerdb.CreateRunnerParams{
		Name:              in.Name,
		OwnerUserID:       ownerArg,
		Visibility:        string(in.Visibility),
		EnrollTokenPrefix: enroll.Prefix,
		EnrollTokenHash:   enroll.Hash,
		ActorID:           in.ActorID,
	})
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrRunnerConflict
		}
		return nil, err
	}
	return runnerFromRow(row), nil
}

func (r *PostgresRepo) GetRunnerByID(ctx context.Context, id int64) (*domain.Runner, error) {
	row, err := r.q.GetRunnerByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRunnerNotFound
		}
		return nil, err
	}
	return runnerFromRow(row), nil
}

func (r *PostgresRepo) ListRunners(ctx context.Context, ownerUserID *int64, visibility *domain.Visibility) ([]*domain.Runner, error) {
	var (
		ownerArg pgtype.Int8
		visArg   pgtype.Text
	)
	if ownerUserID != nil {
		ownerArg = pgtype.Int8{Int64: *ownerUserID, Valid: true}
	}
	if visibility != nil {
		visArg = pgtype.Text{String: string(*visibility), Valid: true}
	}
	rows, err := r.q.ListRunners(ctx, runnerdb.ListRunnersParams{
		OwnerUserID: ownerArg,
		Visibility:  visArg,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Runner, 0, len(rows))
	for _, row := range rows {
		out = append(out, runnerFromRow(row))
	}
	return out, nil
}

func (r *PostgresRepo) DisableRunner(ctx context.Context, id int64) error {
	n, err := r.q.DisableRunner(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrRunnerNotFound
	}
	return nil
}

func (r *PostgresRepo) DeleteRunner(ctx context.Context, id int64) error {
	n, err := r.q.DeleteRunner(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrRunnerNotFound
	}
	return nil
}

func (r *PostgresRepo) UpdateRunnerHeartbeat(ctx context.Context, id int64, capabilities []byte) error {
	if len(capabilities) == 0 {
		capabilities = []byte("{}")
	}
	n, err := r.q.UpdateRunnerHeartbeat(ctx, runnerdb.UpdateRunnerHeartbeatParams{
		Capabilities: capabilities,
		ID:           id,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrRunnerNotFound
	}
	return nil
}

// RedeemEnrollment runs the one-shot redemption transaction. The
// surrounding service layer owns:
//
//   - Wire-format validation (regex + split on the plaintext).
//   - bcrypt comparison of the supplied secret — passed in here as
//     the `verify` closure so it executes under the row lock.
//   - Agent token minting (new prefix + bcrypt(new secret)) — passed
//     in as `newAgent`.
//
// Repo owns:
//
//   - The transaction itself: SELECT FOR UPDATE → status/used-at
//     gate → verify() → UPDATE → COMMIT.
//   - State-machine guards that race-protect concurrent redemptions
//     (disabled runner → ErrRunnerDisabled; already used →
//     ErrEnrollUsed; row missing → ErrInvalidToken).
//
// Returns the fresh Runner row (post-UPDATE). The service composes
// the wire plaintext alongside this row before returning to the
// handler.
func (r *PostgresRepo) RedeemEnrollment(
	ctx context.Context,
	enrollPrefix string,
	verify func(stored *domain.Runner) error,
	newAgent domain.NewAgentToken,
	capabilities []byte,
) (*domain.Runner, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	qtx := r.q.WithTx(tx)

	row, err := qtx.GetRunnerByEnrollPrefixForUpdate(ctx, enrollPrefix)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrInvalidToken
		}
		return nil, err
	}
	rr := runnerFromRow(row)
	if rr.Status == domain.StatusDisabled {
		return nil, domain.ErrRunnerDisabled
	}
	if rr.EnrollTokenUsedAt != nil {
		return nil, domain.ErrEnrollUsed
	}
	if err := verify(rr); err != nil {
		return nil, err
	}
	if len(capabilities) == 0 {
		capabilities = []byte("{}")
	}
	if err := qtx.RedeemEnrollmentUpdate(ctx, runnerdb.RedeemEnrollmentUpdateParams{
		AgentTokenPrefix: pgtype.Text{String: newAgent.Prefix, Valid: true},
		AgentTokenHash:   pgtype.Text{String: newAgent.Hash, Valid: true},
		Capabilities:     capabilities,
		ID:               rr.ID,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return r.GetRunnerByID(ctx, rr.ID)
}

// GetRunnerByAgentTokenPrefix exposes the narrow lookup the service-
// layer AgentTokenValidator needs. Returns ErrRunnerNotFound when no
// row matches so the validator can map it to ErrInvalidToken without
// leaking pgx specifics.
func (r *PostgresRepo) GetRunnerByAgentTokenPrefix(ctx context.Context, prefix string) (*domain.Runner, error) {
	row, err := r.q.GetRunnerByAgentPrefix(ctx, pgtype.Text{String: prefix, Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRunnerNotFound
		}
		return nil, err
	}
	return runnerFromRow(row), nil
}

// GetSessionByTokenPrefix exposes the narrow lookup the service-layer
// SessionTokenValidator needs. Same error-mapping rationale as the
// agent-token variant above.
func (r *PostgresRepo) GetSessionByTokenPrefix(ctx context.Context, prefix string) (*domain.AgentSession, error) {
	row, err := r.q.GetSessionByTokenPrefix(ctx, prefix)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrSessionNotFound
		}
		return nil, err
	}
	return r.sessionFromRow(ctx, row), nil
}

// ---- sessions ----

func (r *PostgresRepo) CreateSession(ctx context.Context, in domain.CreateSessionInput) (*domain.AgentSession, error) {
	if in.SessionTokenPrefix == "" || in.SessionTokenHash == "" {
		return nil, fmt.Errorf("CreateSession: session token prefix/hash required")
	}
	envJSON, err := encodeEnv(in.Env)
	if err != nil {
		return nil, err
	}
	var (
		runnerArg pgtype.Int8
		repoArg   pgtype.Int8
		issueArg  pgtype.Int4
		sealedArg pgtype.Text
	)
	if in.RunnerID != nil {
		runnerArg = pgtype.Int8{Int64: *in.RunnerID, Valid: true}
	}
	if in.RepoID != nil {
		repoArg = pgtype.Int8{Int64: *in.RepoID, Valid: true}
	}
	if in.IssueNumber != nil {
		issueArg = pgtype.Int4{Int32: *in.IssueNumber, Valid: true}
	}
	if in.SessionTokenSealed != "" {
		sealedArg = pgtype.Text{String: in.SessionTokenSealed, Valid: true}
	}
	roleConfig := in.RoleConfig
	if len(roleConfig) == 0 {
		roleConfig = []byte("{}")
	}
	row, err := r.q.CreateSession(ctx, runnerdb.CreateSessionParams{
		RunnerID:           runnerArg,
		RepoID:             repoArg,
		IssueNumber:        issueArg,
		Role:               in.Role,
		Model:              in.Model,
		AgentImage:         in.AgentImage,
		WorkingBranch:      in.WorkingBranch,
		BaseBranch:         in.BaseBranch,
		HostAddendum:       in.HostAddendum,
		Env:                envJSON,
		SessionTokenPrefix: in.SessionTokenPrefix,
		SessionTokenHash:   in.SessionTokenHash,
		SessionTokenSealed: sealedArg,
		CreatedByActorID:   in.CreatedByActorID,
		RepoSha:            in.RepoSHA,
		RoleKey:            in.RoleKey,
		CauseKind:          in.CauseKind,
		CauseID:            in.CauseID,
		RoleConfig:         roleConfig,
	})
	if err != nil {
		return nil, err
	}
	return r.sessionFromRow(ctx, row), nil
}

func (r *PostgresRepo) GetSessionByID(ctx context.Context, id int64) (*domain.AgentSession, error) {
	row, err := r.q.GetSessionByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrSessionNotFound
		}
		return nil, err
	}
	return r.sessionFromRow(ctx, row), nil
}

func (r *PostgresRepo) ListSessions(ctx context.Context, runnerID *int64, status *domain.SessionStatus, limit int) ([]*domain.AgentSession, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		runnerArg pgtype.Int8
		statusArg pgtype.Text
	)
	if runnerID != nil {
		runnerArg = pgtype.Int8{Int64: *runnerID, Valid: true}
	}
	if status != nil {
		statusArg = pgtype.Text{String: string(*status), Valid: true}
	}
	rows, err := r.q.ListSessions(ctx, runnerdb.ListSessionsParams{
		RunnerID: runnerArg,
		Status:   statusArg,
		Lim:      int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.AgentSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, r.sessionFromRow(ctx, row))
	}
	return out, nil
}

// ListSessionsByIssue returns every agent_session for a (repo, issue) tuple
// in spawn order. Powers the agent_session orchestrator: spawn
// idempotency (skip a role with an existing row for the issue) and the
// audit query view.
func (r *PostgresRepo) ListSessionsByIssue(ctx context.Context, repoID int64, issueNumber int32) ([]*domain.AgentSession, error) {
	rows, err := r.q.ListSessionsByIssue(ctx, runnerdb.ListSessionsByIssueParams{
		RepoID:      pgtype.Int8{Int64: repoID, Valid: true},
		IssueNumber: pgtype.Int4{Int32: issueNumber, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.AgentSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, r.sessionFromRow(ctx, row))
	}
	return out, nil
}

// ListRecentSessions returns one page of agent_sessions across the platform
// with optional filters. Powers the admin global audit view — when all
// filters are nil it's a "show me the last N sessions" feed.
func (r *PostgresRepo) ListRecentSessions(ctx context.Context, filter domain.SessionFilter, page domain.SessionPage) ([]*domain.AgentSession, error) {
	limit := page.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := page.Offset
	if offset < 0 {
		offset = 0
	}
	params := runnerdb.ListRecentSessionsParams{Lim: int32(limit), Off: int32(offset)}
	applyRecentSessionFilter(&params.RoleKey, &params.Status, &params.RepoID, &params.Since, filter)
	rows, err := r.q.ListRecentSessions(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.AgentSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, r.sessionFromRow(ctx, row))
	}
	return out, nil
}

// CountRecentSessions counts rows matching the same filter set as
// ListRecentSessions. Windowing knobs are intentionally ignored — the page
// component needs the unbounded total.
func (r *PostgresRepo) CountRecentSessions(ctx context.Context, filter domain.SessionFilter) (int64, error) {
	var params runnerdb.CountRecentSessionsParams
	applyRecentSessionFilter(&params.RoleKey, &params.Status, &params.RepoID, &params.Since, filter)
	return r.q.CountRecentSessions(ctx, params)
}

func applyRecentSessionFilter(roleKey, status *pgtype.Text, repoID *pgtype.Int8, since *pgtype.Timestamptz, filter domain.SessionFilter) {
	if filter.RoleKey != nil {
		*roleKey = pgtype.Text{String: *filter.RoleKey, Valid: true}
	}
	if filter.Status != nil {
		*status = pgtype.Text{String: *filter.Status, Valid: true}
	}
	if filter.RepoID != nil {
		*repoID = pgtype.Int8{Int64: *filter.RepoID, Valid: true}
	}
	if filter.Since != nil {
		*since = pgtype.Timestamptz{Time: *filter.Since, Valid: true}
	}
}

// ArchiveSessionsByIssue flips every non-archived session for the (repo,
// issue) tuple to 'archived'. Idempotent — re-running on an already-
// archived set is a zero-row update.
func (r *PostgresRepo) ArchiveSessionsByIssue(ctx context.Context, repoID int64, issueNumber int32) (int64, error) {
	return r.q.ArchiveSessionsByIssue(ctx, runnerdb.ArchiveSessionsByIssueParams{
		RepoID:      pgtype.Int8{Int64: repoID, Valid: true},
		IssueNumber: pgtype.Int4{Int32: issueNumber, Valid: true},
	})
}

// ArchiveSessionByID archives one session by id (and flags any live
// container for runner cleanup in the same UPDATE). Idempotent on
// already-archived rows; returns nil even when zero rows match.
func (r *PostgresRepo) ArchiveSessionByID(ctx context.Context, id int64) error {
	_, err := r.q.ArchiveSessionByID(ctx, id)
	return err
}

// ClaimNextSession picks the oldest pending session pinned to the runner
// (or any unpinned session) and flips it to 'claimed'. A returning rowless
// case is ErrNoPendingSession — the runner long-poller treats that as
// "wait and retry" rather than a hard error.
func (r *PostgresRepo) ClaimNextSession(ctx context.Context, runnerID int64) (*domain.AgentSession, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	qtx := r.q.WithTx(tx)

	row, err := qtx.ClaimNextSessionLock(ctx, pgtype.Int8{Int64: runnerID, Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNoPendingSession
		}
		return nil, err
	}
	if err := qtx.ClaimSessionUpdate(ctx, runnerdb.ClaimSessionUpdateParams{
		ID:       row.ID,
		RunnerID: pgtype.Int8{Int64: runnerID, Valid: true},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s := r.sessionFromRow(ctx, row)
	s.Status = domain.SessionStatusClaimed
	now := time.Now()
	s.ClaimedAt = &now
	// Reflect the runner_id we just wrote so callers see the pinned
	// row even before re-querying.
	pinned := runnerID
	s.RunnerID = &pinned
	return s, nil
}

func (r *PostgresRepo) MarkSessionRunning(ctx context.Context, id int64) error {
	n, err := r.q.MarkSessionRunning(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrSessionStateInvalid
	}
	return nil
}

// MarkSessionTerminal flips the session into a terminal state and
// preserves session_token_sealed — rewake (Resume/Recover) can still hand
// the same HANGRIX_SESSION_TOKEN back to the next container so that the
// cloned .git/config credential.helper keeps working. The token row
// remains for audit (prefix + hash); the validator's SessionTokenActive()
// check rejects it via terminal-status. The only paths that genuinely
// retire a session forever are ArchiveSessions*; those NULL sealed.
func (r *PostgresRepo) MarkSessionTerminal(ctx context.Context, id int64, status domain.SessionStatus, exitCode *int32, errMsg string) error {
	if !status.Terminal() {
		return fmt.Errorf("status %q is not terminal", status)
	}
	var ec pgtype.Int4
	if exitCode != nil {
		ec = pgtype.Int4{Int32: *exitCode, Valid: true}
	}
	n, err := r.q.MarkSessionTerminal(ctx, runnerdb.MarkSessionTerminalParams{
		Status:       string(status),
		ExitCode:     ec,
		ErrorMessage: errMsg,
		ID:           id,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrSessionStateInvalid
	}
	return nil
}

// MarkSessionIdle flips a claimed/running session to 'idle' without
// touching session_token_sealed. See domain.Repo for the contract: the
// sealed plaintext must survive the container exit so a rewake re-uses
// the same identity.
func (r *PostgresRepo) MarkSessionIdle(ctx context.Context, id int64, exitCode *int32) error {
	var ec pgtype.Int4
	if exitCode != nil {
		ec = pgtype.Int4{Int32: *exitCode, Valid: true}
	}
	n, err := r.q.MarkSessionIdle(ctx, runnerdb.MarkSessionIdleParams{
		ExitCode: ec,
		ID:       id,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrSessionStateInvalid
	}
	return nil
}

// ResumeSession installs a freshly minted session token on an
// idle/failed/succeeded row and flips it back to 'pending'. Returns
// ErrSessionStateInvalid for archived (or already-pending) rows so the
// caller can surface a 409.
func (r *PostgresRepo) ResumeSession(ctx context.Context, id int64, tok domain.NewSessionToken) error {
	n, err := r.q.ResumeSession(ctx, runnerdb.ResumeSessionParams{
		SessionTokenPrefix: tok.Prefix,
		SessionTokenHash:   tok.Hash,
		SessionTokenSealed: pgtype.Text{String: tok.Sealed, Valid: tok.Sealed != ""},
		ID:                 id,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrSessionStateInvalid
	}
	return nil
}

// DeleteSession hard-removes the row. ON DELETE CASCADE wipes the
// message log + inputs queue too.
func (r *PostgresRepo) DeleteSession(ctx context.Context, id int64) error {
	n, err := r.q.DeleteSession(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrSessionNotFound
	}
	return nil
}

// ---- container lifecycle ----

// SetSessionContainer records the long-lived container id the runner is
// using and bumps container_last_used_at. The runner posts this from its
// session driver on every successful Start (cheap, idempotent, drives the
// 7-day idle reaper's freshness check).
func (r *PostgresRepo) SetSessionContainer(ctx context.Context, sessionID int64, containerID string) error {
	n, err := r.q.SetSessionContainer(ctx, runnerdb.SetSessionContainerParams{
		ContainerID: containerID,
		ID:          sessionID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrSessionNotFound
	}
	return nil
}

// PingSession bumps container_last_used_at so roster_list last_activity_at
// reflects real-time liveness between agent interactions.
func (r *PostgresRepo) PingSession(ctx context.Context, sessionID int64) error {
	n, err := r.q.PingSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrSessionNotFound
	}
	return nil
}

// FlagSessionContainerCleanup marks one session's container as needing
// runner-side reaping. Returns ErrSessionNotFound only when the row truly
// does not exist — when the row exists but has no live container, the
// UPDATE matches zero rows and we silently no-op (nothing to clean).
// Callers (Controller.Delete, future force-kill admin) don't need to
// distinguish those cases.
func (r *PostgresRepo) FlagSessionContainerCleanup(ctx context.Context, sessionID int64) error {
	n, err := r.q.FlagSessionContainerCleanup(ctx, sessionID)
	if err != nil {
		return err
	}
	if n == 0 {
		// Either the row is gone, or it had no live container. Probe
		// existence so the caller's error path is meaningful — the
		// extra round-trip is fine for a low-frequency op.
		if _, err := r.q.GetSessionByID(ctx, sessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrSessionNotFound
			}
			return err
		}
	}
	return nil
}

// ListPendingContainerCleanups powers the runner's cleanup-tasks poll.
// The partial index keeps this O(flagged rows on this runner), so the
// runner can poll cheaply on a short interval.
func (r *PostgresRepo) ListPendingContainerCleanups(ctx context.Context, runnerID int64, limit int) ([]domain.ContainerCleanupTask, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.ListPendingContainerCleanups(ctx, runnerdb.ListPendingContainerCleanupsParams{
		RunnerID: pgtype.Int8{Int64: runnerID, Valid: true},
		Lim:      int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.ContainerCleanupTask, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.ContainerCleanupTask{
			SessionID:   row.ID,
			ContainerID: row.ContainerID,
		})
	}
	return out, nil
}

// ClearSessionContainer is the runner's ACK that `docker rm` succeeded.
// We scope the UPDATE by runner_id so a runner that doesn't own the
// session can't clear its sibling's column (defence-in-depth against a
// stolen agent token).
func (r *PostgresRepo) ClearSessionContainer(ctx context.Context, sessionID, ownerRunnerID int64) error {
	n, err := r.q.ClearSessionContainer(ctx, runnerdb.ClearSessionContainerParams{
		ID:       sessionID,
		RunnerID: pgtype.Int8{Int64: ownerRunnerID, Valid: true},
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrSessionNotFound
	}
	return nil
}

// SweepIdleSessionContainers flags every live container whose session has
// been idle (in a non-running terminal-ish state) for longer than
// `threshold`. The platform reaper calls this on a 1-hour ticker; the
// runner picks the flagged rows up on its next cleanup poll.
func (r *PostgresRepo) SweepIdleSessionContainers(ctx context.Context, threshold time.Duration) (int64, error) {
	return r.q.SweepIdleSessionContainers(ctx, pgtype.Interval{Microseconds: threshold.Microseconds(), Valid: true})
}

// SweepAbandonedSessionContainers clears the container_id column for
// sessions that have been flagged for cleanup for longer than
// `threshold` with no runner pickup. After this fires, the container is
// orphaned on the host (if the host still exists), but the session row
// stops claiming ownership of it so user-facing affordances can show it
// as fully gone.
func (r *PostgresRepo) SweepAbandonedSessionContainers(ctx context.Context, threshold time.Duration) (int64, error) {
	return r.q.SweepAbandonedSessionContainers(ctx, pgtype.Interval{Microseconds: threshold.Microseconds(), Valid: true})
}

// ---- container stop lifecycle (migration 00005) ----

// FlagSessionContainerStop marks one session's container for runner-side
// `docker stop`. No-op when the row has no live container or stop is
// already pending.
func (r *PostgresRepo) FlagSessionContainerStop(ctx context.Context, sessionID int64) error {
	n, err := r.q.FlagSessionContainerStop(ctx, sessionID)
	if err != nil {
		return err
	}
	if n == 0 {
		// Either the row is gone, or it had no live container / stop
		// was already pending. Probe existence so the caller's error
		// path is meaningful.
		if _, err := r.q.GetSessionByID(ctx, sessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrSessionNotFound
			}
			return err
		}
	}
	return nil
}

// ListPendingContainerStops powers the runner's stop-tasks poll. The
// partial index keeps this O(flagged rows on this runner).
func (r *PostgresRepo) ListPendingContainerStops(ctx context.Context, runnerID int64, limit int) ([]domain.ContainerStopTask, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.ListPendingContainerStops(ctx, runnerdb.ListPendingContainerStopsParams{
		RunnerID: pgtype.Int8{Int64: runnerID, Valid: true},
		Lim:      int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.ContainerStopTask, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.ContainerStopTask{
			SessionID:   row.ID,
			ContainerID: row.ContainerID,
		})
	}
	return out, nil
}

// AckContainerStop is the runner's ACK that `docker stop` succeeded.
func (r *PostgresRepo) AckContainerStop(ctx context.Context, sessionID, ownerRunnerID int64) error {
	n, err := r.q.AckContainerStop(ctx, runnerdb.AckContainerStopParams{
		ID:       sessionID,
		RunnerID: pgtype.Int8{Int64: ownerRunnerID, Valid: true},
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrSessionNotFound
	}
	return nil
}

// SweepIdleSessionContainersForStop flags every live container whose
// session has been idle longer than `threshold` for a `docker stop`.
func (r *PostgresRepo) SweepIdleSessionContainersForStop(ctx context.Context, threshold time.Duration) (int64, error) {
	return r.q.SweepIdleSessionContainersForStop(ctx, pgtype.Interval{Microseconds: threshold.Microseconds(), Valid: true})
}

// ---- messages ----

// AppendMessage assigns the next seq in (session_id) and inserts the row.
// COALESCE(MAX(seq)+1, 1) is racy under concurrent appends; the unique
// constraint on (session_id, seq) is the second line of defence and we
// retry on conflict. There's only one writer per session (the runner
// forwarding stdout), so the retry path is rarely hit in practice.
func (r *PostgresRepo) AppendMessage(ctx context.Context, m *domain.Message) (*domain.Message, error) {
	if !m.Kind.Valid() {
		return nil, fmt.Errorf("invalid message kind %q", m.Kind)
	}
	payload := m.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	for range 3 {
		row, err := r.q.AppendMessage(ctx, runnerdb.AppendMessageParams{
			SessionID:  m.SessionID,
			Kind:       string(m.Kind),
			Role:       m.Role,
			Content:    m.Content,
			EventName:  m.EventName,
			ToolCallID: m.ToolCallID,
			ToolName:   m.ToolName,
			Payload:    payload,
		})
		if err != nil {
			if database.IsUniqueViolation(err) {
				continue
			}
			return nil, err
		}
		return messageFromRow(row), nil
	}
	return nil, fmt.Errorf("append message: exhausted seq retries")
}

func (r *PostgresRepo) ListMessages(ctx context.Context, sessionID int64) ([]*domain.Message, error) {
	rows, err := r.q.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, messageFromRow(row))
	}
	return out, nil
}

// ---- inputs ----

func (r *PostgresRepo) EnqueueInput(ctx context.Context, sessionID int64, payload []byte) (*domain.SessionInput, error) {
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	for range 3 {
		row, err := r.q.EnqueueInput(ctx, runnerdb.EnqueueInputParams{
			SessionID: sessionID,
			Payload:   payload,
		})
		if err != nil {
			if database.IsUniqueViolation(err) {
				continue
			}
			return nil, err
		}
		return inputFromRow(row), nil
	}
	return nil, fmt.Errorf("enqueue input: exhausted seq retries")
}

// ClaimPendingInputs reads up to `limit` un-consumed inputs in seq order
// and marks them consumed. Used by the runner long-poll: zero rows means
// "nothing new"; the runner just waits and retries.
func (r *PostgresRepo) ClaimPendingInputs(ctx context.Context, sessionID int64, limit int) ([]*domain.SessionInput, error) {
	if limit <= 0 {
		limit = 50
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	qtx := r.q.WithTx(tx)
	rows, err := qtx.ClaimPendingInputsLock(ctx, runnerdb.ClaimPendingInputsLockParams{
		SessionID: sessionID,
		Lim:       int32(limit),
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		_ = tx.Commit(ctx)
		return []*domain.SessionInput{}, nil
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if err := qtx.MarkInputsConsumed(ctx, ids); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]*domain.SessionInput, 0, len(rows))
	for _, row := range rows {
		in := inputFromRow(row)
		in.ConsumedAt = &now
		out = append(out, in)
	}
	return out, nil
}

// ---- helpers ----

func encodeEnv(env map[string]string) ([]byte, error) {
	if len(env) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(env)
}

// ---- row → domain ----

func runnerFromRow(r runnerdb.Runner) *domain.Runner {
	out := &domain.Runner{
		ID:                r.ID,
		Name:              r.Name,
		Visibility:        domain.Visibility(r.Visibility),
		Status:            domain.Status(r.Status),
		Capabilities:      r.Capabilities,
		EnrollTokenPrefix: r.EnrollTokenPrefix,
		EnrollTokenHash:   r.EnrollTokenHash,
		ActorID:           r.ActorID,
		CreatedAt:         r.CreatedAt.Time,
		UpdatedAt:         r.UpdatedAt.Time,
	}
	if r.OwnerUserID.Valid {
		v := r.OwnerUserID.Int64
		out.OwnerUserID = &v
	}
	if r.LastHeartbeatAt.Valid {
		v := r.LastHeartbeatAt.Time
		out.LastHeartbeatAt = &v
	}
	if r.EnrollTokenUsedAt.Valid {
		v := r.EnrollTokenUsedAt.Time
		out.EnrollTokenUsedAt = &v
	}
	if r.AgentTokenPrefix.Valid {
		out.AgentTokenPrefix = r.AgentTokenPrefix.String
	}
	if r.AgentTokenHash.Valid {
		out.AgentTokenHash = r.AgentTokenHash.String
	}
	if r.AgentTokenRevokedAt.Valid {
		v := r.AgentTokenRevokedAt.Time
		out.AgentTokenRevokedAt = &v
	}
	return out
}

// sessionFromRow converts a generated AgentSession row to the domain type.
// It resolves the legacy CreatedBy user_id from the actor table when
// CreatedByActorID > 0. Pass ctx = context.Background() if no request context
// is available.
func (r *PostgresRepo) sessionFromRow(ctx context.Context, row runnerdb.AgentSession) *domain.AgentSession {
	createdBy := int64(0)
	if row.CreatedByActorID > 0 {
		uid, err := r.q.GetActorUserID(ctx, row.CreatedByActorID)
		if err == nil {
			createdBy = uid
		}
	}
	out := &domain.AgentSession{
		ID:                      row.ID,
		Status:                  domain.SessionStatus(row.Status),
		Role:                    row.Role,
		Model:                   row.Model,
		AgentImage:              row.AgentImage,
		WorkingBranch:           row.WorkingBranch,
		BaseBranch:              row.BaseBranch,
		HostAddendum:            row.HostAddendum,
		Env:                     row.Env,
		SessionTokenPrefix:      row.SessionTokenPrefix,
		SessionTokenHash:        row.SessionTokenHash,
		ErrorMessage:            row.ErrorMessage,
		CreatedBy:               createdBy,
		CreatedByActorID:        row.CreatedByActorID,
		CreatedAt:               row.CreatedAt.Time,
		RepoSHA:                 row.RepoSha,
		RoleKey:                 row.RoleKey,
		CauseKind:               row.CauseKind,
		CauseID:                 row.CauseID,
		RoleConfig:              row.RoleConfig,
		ContainerID:             row.ContainerID,
		ContainerCleanupPending: row.ContainerCleanupPending,
		ContainerStopPending:    row.ContainerStopPending,
		RunningJobs:             row.RunningJobs,
	}
	if row.ContainerLastUsedAt.Valid {
		v := row.ContainerLastUsedAt.Time
		out.ContainerLastUsedAt = &v
	}
	if row.ContainerStoppedAt.Valid {
		v := row.ContainerStoppedAt.Time
		out.ContainerStoppedAt = &v
	}
	if row.RunnerID.Valid {
		v := row.RunnerID.Int64
		out.RunnerID = &v
	}
	if row.RepoID.Valid {
		v := row.RepoID.Int64
		out.RepoID = &v
	}
	if row.IssueNumber.Valid {
		v := row.IssueNumber.Int32
		out.IssueNumber = &v
	}
	if row.SessionTokenSealed.Valid {
		out.SessionTokenSealed = row.SessionTokenSealed.String
	}
	if row.SessionTokenRevokedAt.Valid {
		v := row.SessionTokenRevokedAt.Time
		out.SessionTokenRevokedAt = &v
	}
	if row.ExitCode.Valid {
		v := row.ExitCode.Int32
		out.ExitCode = &v
	}
	if row.ClaimedAt.Valid {
		v := row.ClaimedAt.Time
		out.ClaimedAt = &v
	}
	if row.StartedAt.Valid {
		v := row.StartedAt.Time
		out.StartedAt = &v
	}
	if row.EndedAt.Valid {
		v := row.EndedAt.Time
		out.EndedAt = &v
	}
	return out
}

func messageFromRow(r runnerdb.AgentSessionMessage) *domain.Message {
	return &domain.Message{
		ID:         r.ID,
		SessionID:  r.SessionID,
		Seq:        r.Seq,
		Kind:       domain.MessageKind(r.Kind),
		Role:       r.Role,
		Content:    r.Content,
		EventName:  r.EventName,
		ToolCallID: r.ToolCallID,
		ToolName:   r.ToolName,
		Payload:    r.Payload,
		CreatedAt:  r.CreatedAt.Time,
	}
}

func inputFromRow(r runnerdb.AgentSessionInput) *domain.SessionInput {
	out := &domain.SessionInput{
		ID:        r.ID,
		SessionID: r.SessionID,
		Seq:       r.Seq,
		Payload:   r.Payload,
		CreatedAt: r.CreatedAt.Time,
	}
	if r.ConsumedAt.Valid {
		v := r.ConsumedAt.Time
		out.ConsumedAt = &v
	}
	return out
}
