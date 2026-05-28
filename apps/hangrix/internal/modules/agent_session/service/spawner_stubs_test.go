package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	gitdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/git/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// stubBlob feeds the spawner pre-canned bytes for each (ref, path).
// Tests register both the host yaml + the lock file the same way the
// production GitBlobReader would return them.
type stubBlob struct {
	files map[string][]byte // key: ref + ":" + path
}

func (s *stubBlob) ReadBlob(_ context.Context, _, ref, path string) ([]byte, bool) {
	if s.files == nil {
		return nil, false
	}
	body, ok := s.files[ref+":"+path]
	return body, ok
}

// ListBlobs mirrors GitBlobReader.ListBlobs over the in-memory files map:
// it returns the repo-relative paths of the entries directly under
// <ref>:<dir>. Keys are "<ref>:<path>"; we match the "<ref>:<dir>/"
// prefix, strip the "<ref>:" so the returned paths are repo-relative,
// and only emit direct children (no deeper nesting). (nil,false) when
// the dir has no entries — same stance as the production reader.
func (s *stubBlob) ListBlobs(_ context.Context, _, ref, dir string) ([]string, bool) {
	if s.files == nil {
		return nil, false
	}
	dir = strings.TrimSuffix(dir, "/")
	keyPrefix := ref + ":" + dir + "/"
	var out []string
	for key := range s.files {
		if !strings.HasPrefix(key, keyPrefix) {
			continue
		}
		rest := strings.TrimPrefix(key, keyPrefix)
		// Direct children only.
		if strings.Contains(rest, "/") {
			continue
		}
		// Repo-relative path = key minus the "<ref>:" prefix.
		out = append(out, strings.TrimPrefix(key, ref+":"))
	}
	if len(out) == 0 {
		return nil, false
	}
	sort.Strings(out)
	return out, true
}

func newStubBlob(files map[string][]byte) *stubBlob {
	return &stubBlob{files: files}
}

// stubRepoStore satisfies repodomain.Store. Only GetByID / GetByOwnerAndName
// are exercised by the spawner; the rest panic so a future change that
// pulls in a new dependency lights up loudly. Mirrors the
// stubKindStore pattern in repo/handler/git_http_kind_test.go.
type stubRepoStore struct {
	byID        map[int64]*repodomain.Repo
	byOwnerName map[string]*repodomain.Repo // key: ownerName + "/" + name
}

func newStubRepoStore() *stubRepoStore {
	return &stubRepoStore{
		byID:        map[int64]*repodomain.Repo{},
		byOwnerName: map[string]*repodomain.Repo{},
	}
}

func (s *stubRepoStore) add(repo *repodomain.Repo) {
	s.byID[repo.ID] = repo
	s.byOwnerName[repo.OwnerName+"/"+repo.Name] = repo
}

func (s *stubRepoStore) GetByID(_ context.Context, id int64) (*repodomain.Repo, error) {
	if r, ok := s.byID[id]; ok {
		return r, nil
	}
	return nil, repodomain.ErrRepoNotFound
}

func (s *stubRepoStore) GetByOwnerAndName(_ context.Context, _ repodomain.OwnerKind, ownerID int64, name string) (*repodomain.Repo, error) {
	// Tests register by owner name; we walk the map and match on
	// (OwnerID, name).
	for _, r := range s.byID {
		if r.OwnerID == ownerID && r.Name == name {
			return r, nil
		}
	}
	return nil, repodomain.ErrRepoNotFound
}

func (s *stubRepoStore) Create(context.Context, repodomain.OwnerKind, int64, string, string, string, repodomain.Visibility) (*repodomain.Repo, error) {
	panic("Create not stubbed")
}
func (s *stubRepoStore) ListByOwner(context.Context, repodomain.OwnerKind, int64, bool, int32, int32) ([]*repodomain.Repo, int64, error) {
	panic("ListByOwner not stubbed")
}
func (s *stubRepoStore) Delete(context.Context, int64) error { panic("Delete not stubbed") }
func (s *stubRepoStore) UpdateMeta(context.Context, int64, string, string, repodomain.Visibility) (*repodomain.Repo, error) {
	panic("UpdateMeta not stubbed")
}
func (s *stubRepoStore) Transfer(context.Context, int64, repodomain.OwnerKind, int64) (*repodomain.Repo, error) {
	panic("Transfer not stubbed")
}

// stubResolver satisfies orgdomain.Resolver. Only ResolveOwner is
// exercised; the rest panic.
type stubResolver struct {
	owners map[string]orgdomain.Owner
}

func newStubResolver() *stubResolver { return &stubResolver{owners: map[string]orgdomain.Owner{}} }

func (s *stubResolver) addUser(name string, id int64) {
	s.owners[name] = orgdomain.Owner{Kind: orgdomain.OwnerKindUser, ID: id, Name: name}
}

func (s *stubResolver) ResolveOwner(_ context.Context, name string) (*orgdomain.Owner, error) {
	if o, ok := s.owners[name]; ok {
		return &o, nil
	}
	return nil, orgdomain.ErrOwnerNotFound
}

func (s *stubResolver) Membership(context.Context, int64, int64) (orgdomain.Role, bool, error) {
	panic("Membership not stubbed")
}

// stubPathResolver maps (owner, name) → a deterministic fake fs path so
// blob lookups in stubBlob can be keyed off the same string the spawner
// produced. Real filesystem isn't touched by tests.
type stubPathResolver struct{}

func (stubPathResolver) ResolvePath(ownerUsername, repoName string) (string, error) {
	return "/fake/" + ownerUsername + "/" + repoName + ".git", nil
}

// stubGit satisfies gitdomain.Git. Only ResolveCommit is exercised; the
// rest panic so additions to the spawner's git surface fail loudly. We
// drive the return value via a map keyed by `<repo_fs_path>:<ref>`.
type stubGit struct {
	commits map[string]string // key: repoFs + ":" + ref → sha
}

func newStubGit() *stubGit { return &stubGit{commits: map[string]string{}} }

func (g *stubGit) add(fs, ref, sha string) { g.commits[fs+":"+ref] = sha }

func (g *stubGit) ResolveCommit(path, ref string) (string, error) {
	if sha, ok := g.commits[path+":"+ref]; ok {
		return sha, nil
	}
	return "", gitdomain.ErrRefNotFound
}

func (g *stubGit) Init(string, string) error { panic("Init not stubbed") }
func (g *stubGit) SeedInitialCommit(string, string, map[string][]byte, string, string) error {
	panic("SeedInitialCommit not stubbed")
}
func (g *stubGit) ListRefs(string) (*gitdomain.Refs, error) { panic("ListRefs not stubbed") }
func (g *stubGit) ListCommits(string, string, int, int) ([]*gitdomain.Commit, error) {
	panic("ListCommits not stubbed")
}
func (g *stubGit) CommitByID(string, string) (*gitdomain.CommitWithDiff, error) {
	panic("CommitByID not stubbed")
}
func (g *stubGit) Tree(string, string, string) ([]*gitdomain.TreeEntry, error) {
	panic("Tree not stubbed")
}
func (g *stubGit) TreeView(string, string, string) (*gitdomain.TreeView, error) {
	panic("TreeView not stubbed")
}
func (g *stubGit) Blob(string, string, string) ([]byte, bool, error) { panic("Blob not stubbed") }
func (g *stubGit) DiffRefs(string, string, string) ([]*gitdomain.FileDiff, error) {
	panic("DiffRefs not stubbed")
}
func (g *stubGit) DiffMergeBase(string, string, string) ([]*gitdomain.FileDiff, error) {
	panic("DiffMergeBase not stubbed")
}
func (g *stubGit) CreateBranch(string, string, string) error   { panic("CreateBranch not stubbed") }
func (g *stubGit) CreateBranchAt(string, string, string) error { panic("CreateBranchAt not stubbed") }
func (g *stubGit) DeleteBranch(string, string) error           { panic("DeleteBranch not stubbed") }
func (g *stubGit) SetHEAD(string, string) error                { panic("SetHEAD not stubbed") }
func (g *stubGit) CreateLightweightTag(string, string, string) error {
	panic("CreateLightweightTag not stubbed")
}
func (g *stubGit) CreateAnnotatedTag(string, string, string, string, gitdomain.Signature) error {
	panic("CreateAnnotatedTag not stubbed")
}
func (g *stubGit) DeleteTag(string, string) error { panic("DeleteTag not stubbed") }
func (g *stubGit) ContainsCommit(string, string) (*gitdomain.ContainingRefs, error) {
	panic("ContainsCommit not stubbed")
}
func (g *stubGit) IsAncestor(string, string, string) (bool, error) {
	panic("IsAncestor not stubbed")
}

func (g *stubGit) MergeBranch(string, string, string, string, gitdomain.Signature) (string, string, error) {
	panic("MergeBranch not stubbed")
}
func (g *stubGit) CheckFastForward(string, string, string) (bool, string, error) {
	panic("CheckFastForward not stubbed")
}
func (g *stubGit) CheckAutoMerge(string, string, string) (bool, string, string, error) {
	panic("CheckAutoMerge not stubbed")
}

func (g *stubGit) ApplyPatch(string, string, string, string, gitdomain.Signature, gitdomain.Signature) (string, error) {
	panic("ApplyPatch not stubbed")
}
func (g *stubGit) EditAndCommit(string, string, string, string, []byte, string, gitdomain.Signature, gitdomain.Signature) (string, error) {
	panic("EditAndCommit not stubbed")
}

// stubRunnerRepo satisfies runnerdomain.Repo for the spawner / archiver /
// auditor surface. The actual storage layer is the runner module's
// Postgres impl in production; here we keep an in-memory list keyed by
// (RepoID, IssueNumber) for the methods the M7a P2 wiring exercises.
type stubRunnerRepo struct {
	sessions     []*runnerdomain.AgentSession
	messages     []*runnerdomain.Message
	inputs       []*runnerdomain.SessionInput
	nextID       int64
	createErr    error
	archiveCount int64
	resumeErr    error
}

func newStubRunnerRepo() *stubRunnerRepo { return &stubRunnerRepo{nextID: 1} }

func (r *stubRunnerRepo) ListSessionsByIssue(_ context.Context, repoID int64, issueNumber int32) ([]*runnerdomain.AgentSession, error) {
	out := []*runnerdomain.AgentSession{}
	for _, s := range r.sessions {
		if s.RepoID != nil && *s.RepoID == repoID && s.IssueNumber != nil && *s.IssueNumber == issueNumber {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *stubRunnerRepo) ListRecentSessions(_ context.Context, _ runnerdomain.SessionFilter, _ runnerdomain.SessionPage) ([]*runnerdomain.AgentSession, error) {
	return r.sessions, nil
}

func (r *stubRunnerRepo) CountRecentSessions(_ context.Context, _ runnerdomain.SessionFilter) (int64, error) {
	return int64(len(r.sessions)), nil
}

func (r *stubRunnerRepo) DeleteRunner(_ context.Context, _ int64) error { return nil }

func (r *stubRunnerRepo) CreateSession(_ context.Context, in runnerdomain.CreateSessionInput) (*runnerdomain.AgentSession, error) {
	if r.createErr != nil {
		return nil, r.createErr
	}
	id := r.nextID
	r.nextID++
	// JSON-encode env the same way the production infra impl does so
	// audit-style tests can read it back via json.Unmarshal.
	envBytes := []byte("{}")
	if len(in.Env) > 0 {
		b, err := json.Marshal(in.Env)
		if err != nil {
			return nil, fmt.Errorf("stub encode env: %w", err)
		}
		envBytes = b
	}
	sess := &runnerdomain.AgentSession{
		ID:                 id,
		RunnerID:           in.RunnerID,
		RepoID:             in.RepoID,
		IssueNumber:        in.IssueNumber,
		Status:             runnerdomain.SessionStatusPending,
		Role:               in.Role,
		Model:              in.Model,
		AgentImage:         in.AgentImage,
		WorkingBranch:      in.WorkingBranch,
		BaseBranch:         in.BaseBranch,
		HostAddendum:       in.HostAddendum,
		Env:                envBytes,
		SessionTokenPrefix: in.SessionTokenPrefix,
		SessionTokenHash:   in.SessionTokenHash,
		SessionTokenSealed: in.SessionTokenSealed,
		CreatedBy:          in.CreatedBy,
		CreatedAt:          time.Now(),
		RepoSHA:            in.RepoSHA,
		RoleKey:            in.RoleKey,
		CauseKind:          in.CauseKind,
		CauseID:            in.CauseID,
		RoleConfig:         in.RoleConfig,
	}
	r.sessions = append(r.sessions, sess)
	return sess, nil
}

func (r *stubRunnerRepo) EnqueueInput(_ context.Context, sessionID int64, payload []byte) (*runnerdomain.SessionInput, error) {
	in := &runnerdomain.SessionInput{
		ID:        int64(len(r.inputs) + 1),
		SessionID: sessionID,
		Payload:   payload,
		CreatedAt: time.Now(),
	}
	r.inputs = append(r.inputs, in)
	return in, nil
}

func (r *stubRunnerRepo) AppendMessage(_ context.Context, m *runnerdomain.Message) (*runnerdomain.Message, error) {
	m.ID = int64(len(r.messages) + 1)
	m.CreatedAt = time.Now()
	r.messages = append(r.messages, m)
	return m, nil
}

func (r *stubRunnerRepo) ArchiveSessionsByIssue(_ context.Context, repoID int64, issueNumber int32) (int64, error) {
	var n int64
	for _, s := range r.sessions {
		if s.RepoID != nil && *s.RepoID == repoID && s.IssueNumber != nil && *s.IssueNumber == issueNumber && s.Status != runnerdomain.SessionStatusArchived {
			s.Status = runnerdomain.SessionStatusArchived
			n++
		}
	}
	r.archiveCount = n
	return n, nil
}

// --- panicking stubs for the runner Repo methods the spawner / archiver
//     / auditor never call. Keeping them here (rather than skipping the
//     interface assertion) means a future spawner change that pulls in,
//     say, ClaimNextSession blows up obviously instead of silently.

func (r *stubRunnerRepo) CreateRunner(context.Context, runnerdomain.CreateRunnerInput, runnerdomain.NewEnrollToken) (*runnerdomain.Runner, error) {
	panic("CreateRunner not stubbed")
}
func (r *stubRunnerRepo) GetRunnerByID(context.Context, int64) (*runnerdomain.Runner, error) {
	panic("GetRunnerByID not stubbed")
}
func (r *stubRunnerRepo) GetRunnerByAgentTokenPrefix(context.Context, string) (*runnerdomain.Runner, error) {
	panic("GetRunnerByAgentTokenPrefix not stubbed")
}
func (r *stubRunnerRepo) ListRunners(context.Context, *int64, *runnerdomain.Visibility) ([]*runnerdomain.Runner, error) {
	panic("ListRunners not stubbed")
}
func (r *stubRunnerRepo) DisableRunner(context.Context, int64) error {
	panic("DisableRunner not stubbed")
}
func (r *stubRunnerRepo) UpdateRunnerHeartbeat(context.Context, int64, []byte) error {
	panic("UpdateRunnerHeartbeat not stubbed")
}
func (r *stubRunnerRepo) RedeemEnrollment(context.Context, string, func(*runnerdomain.Runner) error, runnerdomain.NewAgentToken, []byte) (*runnerdomain.Runner, error) {
	panic("RedeemEnrollment not stubbed")
}
func (r *stubRunnerRepo) GetSessionByID(_ context.Context, id int64) (*runnerdomain.AgentSession, error) {
	for _, s := range r.sessions {
		if s.ID == id {
			return s, nil
		}
	}
	return nil, runnerdomain.ErrSessionNotFound
}
func (r *stubRunnerRepo) GetSessionByTokenPrefix(context.Context, string) (*runnerdomain.AgentSession, error) {
	panic("GetSessionByTokenPrefix not stubbed")
}
func (r *stubRunnerRepo) ListSessions(context.Context, *int64, *runnerdomain.SessionStatus, int) ([]*runnerdomain.AgentSession, error) {
	panic("ListSessions not stubbed")
}
func (r *stubRunnerRepo) ClaimNextSession(context.Context, int64) (*runnerdomain.AgentSession, error) {
	panic("ClaimNextSession not stubbed")
}
func (r *stubRunnerRepo) MarkSessionRunning(context.Context, int64) error {
	panic("MarkSessionRunning not stubbed")
}
func (r *stubRunnerRepo) MarkSessionTerminal(_ context.Context, id int64, status runnerdomain.SessionStatus, exitCode *int32, errMsg string) error {
	for _, s := range r.sessions {
		if s.ID == id {
			s.Status = status
			s.ExitCode = exitCode
			s.ErrorMessage = errMsg
			return nil
		}
	}
	return runnerdomain.ErrSessionNotFound
}
func (r *stubRunnerRepo) MarkSessionIdle(_ context.Context, id int64, exitCode *int32) error {
	for _, s := range r.sessions {
		if s.ID == id {
			s.Status = runnerdomain.SessionStatusIdle
			s.ExitCode = exitCode
			return nil
		}
	}
	return runnerdomain.ErrSessionNotFound
}
func (r *stubRunnerRepo) ResumeSession(_ context.Context, id int64, tok runnerdomain.NewSessionToken) error {
	if r.resumeErr != nil {
		return r.resumeErr
	}
	for _, s := range r.sessions {
		if s.ID == id {
			s.Status = runnerdomain.SessionStatusPending
			s.SessionTokenPrefix = tok.Prefix
			s.SessionTokenHash = tok.Hash
			s.SessionTokenSealed = tok.Sealed
			s.RunnerID = nil
			s.ClaimedAt = nil
			s.StartedAt = nil
			s.EndedAt = nil
			s.ExitCode = nil
			s.ErrorMessage = ""
			return nil
		}
	}
	return runnerdomain.ErrSessionStateInvalid
}
func (r *stubRunnerRepo) DeleteSession(_ context.Context, id int64) error {
	for i, s := range r.sessions {
		if s.ID == id {
			r.sessions = append(r.sessions[:i], r.sessions[i+1:]...)
			return nil
		}
	}
	return runnerdomain.ErrSessionNotFound
}
func (r *stubRunnerRepo) ListMessages(context.Context, int64) ([]*runnerdomain.Message, error) {
	panic("ListMessages not stubbed")
}
func (r *stubRunnerRepo) ClaimPendingInputs(context.Context, int64, int) ([]*runnerdomain.SessionInput, error) {
	panic("ClaimPendingInputs not stubbed")
}

// Container-lifecycle methods (migration 00004). The spawner under test
// doesn't drive these directly — the runner-facing handler and the
// reaper goroutine do — so the stubs are minimal no-ops sufficient to
// satisfy the Repo interface for unit-test compilation.
func (r *stubRunnerRepo) SetSessionContainer(context.Context, int64, string) error {
	return nil
}
func (r *stubRunnerRepo) PingSession(context.Context, int64) error { return nil }
func (r *stubRunnerRepo) FlagSessionContainerCleanup(context.Context, int64) error {
	return nil
}
func (r *stubRunnerRepo) ListPendingContainerCleanups(context.Context, int64, int) ([]runnerdomain.ContainerCleanupTask, error) {
	return nil, nil
}
func (r *stubRunnerRepo) ClearSessionContainer(context.Context, int64, int64) error {
	return nil
}
func (r *stubRunnerRepo) SweepIdleSessionContainers(context.Context, time.Duration) (int64, error) {
	return 0, nil
}
func (r *stubRunnerRepo) SweepAbandonedSessionContainers(context.Context, time.Duration) (int64, error) {
	return 0, nil
}
func (r *stubRunnerRepo) FlagSessionContainerStop(context.Context, int64) error {
	return nil
}
func (r *stubRunnerRepo) ListPendingContainerStops(context.Context, int64, int) ([]runnerdomain.ContainerStopTask, error) {
	return nil, nil
}
func (r *stubRunnerRepo) AckContainerStop(context.Context, int64, int64) error {
	return nil
}
func (r *stubRunnerRepo) SweepIdleSessionContainersForStop(context.Context, time.Duration) (int64, error) {
	return 0, nil
}
func (r *stubRunnerRepo) ArchiveSessionByID(_ context.Context, id int64) error {
	for _, s := range r.sessions {
		if s.ID == id {
			s.Status = runnerdomain.SessionStatusArchived
			if s.ContainerID != "" {
				s.ContainerCleanupPending = true
			}
			return nil
		}
	}
	return runnerdomain.ErrSessionNotFound
}

// dump is a debug helper unused by tests but handy from a debugger.
func (r *stubRunnerRepo) dump() string {
	return fmt.Sprintf("sessions=%d messages=%d inputs=%d", len(r.sessions), len(r.messages), len(r.inputs))
}
