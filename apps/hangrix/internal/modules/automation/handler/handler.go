// Package handler exposes the automation module's HTTP surface under
// /api/repos/{owner}/{name}/automation. All routes require authentication;
// read operations require repo read access, write operations require repo
// write access (owner / org-owner / admin).
package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/service"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
)

// Handler serves automation config and run history for a single repo.
type Handler struct {
	repoStore   repodomain.Store
	orgResolver orgdomain.Resolver
	orgRepo     orgdomain.OrgRepo
	middleware  authdomain.Middleware
	pathRes     repodomain.PathResolver
	validator   *service.Validator
	executor    *service.Executor
	runStore    domain.Store
}

// HandlerDeps wires the Handler's dependencies through ioc.
type HandlerDeps struct {
	RepoStore   repodomain.Store
	OrgResolver orgdomain.Resolver
	OrgRepo     orgdomain.OrgRepo
	Middleware  authdomain.Middleware
	PathRes     repodomain.PathResolver
	Validator   *service.Validator
	Executor    *service.Executor
	RunStore    domain.Store
}

// NewHandler returns a ready-to-use Handler.
func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		repoStore:   deps.RepoStore,
		orgResolver: deps.OrgResolver,
		orgRepo:     deps.OrgRepo,
		middleware:  deps.Middleware,
		pathRes:     deps.PathRes,
		validator:   deps.Validator,
		executor:    deps.Executor,
		runStore:    deps.RunStore,
	}
}

// RegisterRoutes adds automation routes under /api/repos/{owner}/{name}/automation.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/repos/{owner}/{name}/automation", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/", h.getConfig)
		r.Put("/", h.putConfig)
		r.Post("/{taskName}/trigger", h.trigger)
		r.Get("/runs", h.listRuns)
	})
}

// ---- response types ----

type configResponse struct {
	Tasks []publicTask `json:"tasks"`
	Runs  []publicRun  `json:"runs"`
}

type publicTask struct {
	Name     string      `json:"name"`
	Schedule string      `json:"schedule"`
	Issue    publicIssue `json:"issue"`
	Roles    []string    `json:"roles"`
	Enabled  bool        `json:"enabled"`
}

type publicIssue struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

type publicRun struct {
	ID           int64   `json:"id"`
	TaskName     string  `json:"task_name"`
	IssueID      *int64  `json:"issue_id"`
	Status       string  `json:"status"`
	ErrorMessage *string `json:"error_message"`
	StartedAt    string  `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
	CreatedAt    string  `json:"created_at"`
}

type runsResponse struct {
	Items []publicRun `json:"items"`
}

// ---- GET /api/repos/{owner}/{name}/automation ----

func (h *Handler) getConfig(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}

	fsPath, err := h.pathRes.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "resolve path: "+err.Error())
		return
	}

	// Read the config from git.
	raw, okRead := readBlob(r.Context(), fsPath, repo.DefaultBranch, ".hangrix/automation.yml")
	var tasks []publicTask
	if okRead {
		cfg, err := agentsconfig.ParseAutomationConfig(raw)
		if err == nil {
			for _, t := range cfg.Tasks {
				if t == nil {
					continue
				}
				tasks = append(tasks, publicTask{
					Name:     t.Name,
					Schedule: t.Schedule,
					Issue: publicIssue{
						Title:  t.Issue.Title,
						Body:   t.Issue.Body,
						Labels: t.Issue.Labels,
					},
					Roles:   t.Roles,
					Enabled: t.Enabled,
				})
			}
		}
	}

	// Load recent runs. Return an empty slice rather than null.
	runs, err := h.runStore.ListRuns(r.Context(), repo.ID, "", 10)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list runs: "+err.Error())
		return
	}
	pubRuns := make([]publicRun, 0, len(runs))
	for _, run := range runs {
		pubRuns = append(pubRuns, toPublicRun(run))
	}

	httpx.WriteJSON(w, http.StatusOK, configResponse{
		Tasks: tasks,
		Runs:  pubRuns,
	})
}

// ---- PUT /api/repos/{owner}/{name}/automation ----

func (h *Handler) putConfig(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForWrite(w, r)
	if !ok {
		return
	}

	// Read the raw YAML body (1 MiB cap).
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	// Parse and validate.
	cfg, err := agentsconfig.ParseAutomationConfig(body)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cfg.Validate(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.validator.ValidateConfig(cfg); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Write the config to the repo as a new commit.
	caller, _ := authdomain.UserFromRequest(r)
	fsPath, err := h.pathRes.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "resolve path: "+err.Error())
		return
	}
	if err := writeAutomationYAML(r.Context(), fsPath, repo.DefaultBranch, body,
		caller.Username, caller.Email); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "write config: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---- POST /api/repos/{owner}/{name}/automation/{taskName}/trigger ----

func (h *Handler) trigger(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForWrite(w, r)
	if !ok {
		return
	}

	taskName := chi.URLParam(r, "taskName")
	if taskName == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing task name")
		return
	}

	// Read config to find the task.
	fsPath, err := h.pathRes.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "resolve path: "+err.Error())
		return
	}
	raw, okRead := readBlob(r.Context(), fsPath, repo.DefaultBranch, ".hangrix/automation.yml")
	if !okRead {
		httpx.WriteError(w, http.StatusNotFound, "no automation config found")
		return
	}
	cfg, err := agentsconfig.ParseAutomationConfig(raw)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "parse config: "+err.Error())
		return
	}

	var task *agentsconfig.Task
	for _, t := range cfg.Tasks {
		if t != nil && t.Name == taskName {
			task = t
			break
		}
	}
	if task == nil {
		httpx.WriteError(w, http.StatusNotFound, "task not found")
		return
	}

	// Determine author user ID.
	authorUserID, err := h.resolveAuthorUserID(r.Context(), repo)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "resolve author: "+err.Error())
		return
	}

	// Dedup check.
	exists, err := h.runStore.RecentRunExists(r.Context(), repo.ID, task.Name, 60e9) // 60s in ns
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dedup check: "+err.Error())
		return
	}
	if exists {
		httpx.WriteError(w, http.StatusConflict, "task was triggered within the last 60 seconds; wait before retrying")
		return
	}

	run, err := h.executor.Execute(r.Context(), repo.ID, repo.DefaultBranch, authorUserID, task)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "execute: "+err.Error())
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, toPublicRun(run))
}

// ---- GET /api/repos/{owner}/{name}/automation/runs ----

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}

	taskName := r.URL.Query().Get("task")
	limit := int32(50)
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}

	runs, err := h.runStore.ListRuns(r.Context(), repo.ID, taskName, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list runs: "+err.Error())
		return
	}

	items := make([]publicRun, 0, len(runs))
	for _, run := range runs {
		items = append(items, toPublicRun(run))
	}
	httpx.WriteJSON(w, http.StatusOK, runsResponse{Items: items})
}

// ---- repo resolution helpers ----

func (h *Handler) resolveRepoForRead(w http.ResponseWriter, r *http.Request) (*repodomain.Repo, bool) {
	return h.resolveRepo(w, r, false)
}

func (h *Handler) resolveRepoForWrite(w http.ResponseWriter, r *http.Request) (*repodomain.Repo, bool) {
	return h.resolveRepo(w, r, true)
}

func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request, write bool) (*repodomain.Repo, bool) {
	ownerName := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "name")
	if ownerName == "" || repoName == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing owner or name")
		return nil, false
	}

	owner, err := h.orgResolver.ResolveOwner(r.Context(), ownerName)
	if err != nil {
		if errors.Is(err, orgdomain.ErrOwnerNotFound) || errors.Is(err, orgdomain.ErrOrgReserved) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}

	repo, err := h.repoStore.GetByOwnerAndName(r.Context(), repodomain.OwnerKind(owner.Kind), owner.ID, repoName)
	if err != nil {
		if errors.Is(err, repodomain.ErrRepoNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}

	caller, _ := authdomain.UserFromRequest(r)
	if write {
		can, err := h.canWriteRepo(r.Context(), caller, repo)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return nil, false
		}
		if !can {
			httpx.WriteError(w, http.StatusForbidden, "forbidden")
			return nil, false
		}
	} else if repo.Visibility == repodomain.VisibilityPrivate {
		can, err := h.canReadRepo(r.Context(), caller, repo)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return nil, false
		}
		if !can {
			httpx.WriteError(w, http.StatusForbidden, "forbidden")
			return nil, false
		}
	}
	return repo, true
}

func (h *Handler) canReadRepo(ctx context.Context, caller *userdomain.User, repo *repodomain.Repo) (bool, error) {
	if caller == nil {
		return false, nil
	}
	if caller.Role == userdomain.RoleAdmin {
		return true, nil
	}
	switch repo.OwnerKind {
	case repodomain.OwnerKindUser:
		return caller.ID == repo.OwnerID, nil
	case repodomain.OwnerKindOrg:
		_, ok, err := h.orgResolver.Membership(ctx, repo.OwnerID, caller.ID)
		return ok, err
	}
	return false, nil
}

func (h *Handler) canWriteRepo(ctx context.Context, caller *userdomain.User, repo *repodomain.Repo) (bool, error) {
	if caller == nil {
		return false, nil
	}
	if caller.Role == userdomain.RoleAdmin {
		return true, nil
	}
	switch repo.OwnerKind {
	case repodomain.OwnerKindUser:
		return caller.ID == repo.OwnerID, nil
	case repodomain.OwnerKindOrg:
		role, ok, err := h.orgResolver.Membership(ctx, repo.OwnerID, caller.ID)
		if err != nil || !ok {
			return false, err
		}
		return role == orgdomain.RoleOwner, nil
	}
	return false, nil
}

// resolveAuthorUserID returns the user ID to use as the issue author.
func (h *Handler) resolveAuthorUserID(ctx context.Context, repo *repodomain.Repo) (int64, error) {
	switch repo.OwnerKind {
	case repodomain.OwnerKindUser:
		return repo.OwnerID, nil
	case repodomain.OwnerKindOrg:
		members, err := h.orgRepo.ListMembers(ctx, repo.OwnerID)
		if err != nil {
			return 0, fmt.Errorf("list org members: %w", err)
		}
		for _, m := range members {
			if m.Role == orgdomain.RoleOwner {
				return m.UserID, nil
			}
		}
		return 0, fmt.Errorf("org %d has no owner members", repo.OwnerID)
	}
	return 0, fmt.Errorf("unknown owner kind: %s", repo.OwnerKind)
}

// ---- git helpers ----

func toPublicRun(run *domain.AutomationRun) publicRun {
	pr := publicRun{
		ID:           run.ID,
		TaskName:     run.TaskName,
		IssueID:      run.IssueID,
		Status:       string(run.Status),
		ErrorMessage: run.ErrorMessage,
		StartedAt:    run.StartedAt.Format("2006-01-02T15:04:05Z"),
		CreatedAt:    run.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if run.FinishedAt != nil {
		s := run.FinishedAt.Format("2006-01-02T15:04:05Z")
		pr.FinishedAt = &s
	}
	return pr
}

// readBlob reads a file at ref:path from a bare repo via git cat-file.
func readBlob(ctx context.Context, repoFsPath, ref, path string) ([]byte, bool) {
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

// writeAutomationYAML replaces .hangrix/automation.yml on the default
// branch by creating a new commit via git plumbing commands.
func writeAutomationYAML(ctx context.Context, repoFsPath, branch string, content []byte, authorName, authorEmail string) error {
	gitEnv := []string{"GIT_DIR=" + repoFsPath}
	ref := "refs/heads/" + branch

	// 1. Write the new blob.
	hashObj := exec.CommandContext(ctx, "git", "hash-object", "-w", "--stdin")
	hashObj.Env = gitEnv
	hashObj.Stdin = strings.NewReader(string(content))
	hashObj.Stderr = io.Discard
	blobHashBytes, err := hashObj.Output()
	if err != nil {
		return fmt.Errorf("hash-object: %w", err)
	}
	blobHash := strings.TrimSpace(string(blobHashBytes))
	if blobHash == "" {
		return fmt.Errorf("hash-object: empty hash")
	}

	// 2. Read the current tree into a temporary index, then add the new
	//    blob at .hangrix/automation.yml via update-index --add.
	tmpIndex := repoFsPath + "/tmp-index-automation"
	defer func() { _ = exec.Command("rm", "-f", tmpIndex).Run() }()

	readTree := exec.CommandContext(ctx, "git", "read-tree", ref)
	readTree.Env = append(gitEnv, "GIT_INDEX_FILE="+tmpIndex)
	readTree.Stderr = io.Discard
	if err := readTree.Run(); err != nil {
		return fmt.Errorf("read-tree: %w", err)
	}

	updateIndex := exec.CommandContext(ctx, "git", "update-index", "--add",
		"--cacheinfo", "100644,"+blobHash+",.hangrix/automation.yml")
	updateIndex.Env = append(gitEnv, "GIT_INDEX_FILE="+tmpIndex)
	updateIndex.Stderr = io.Discard
	if err := updateIndex.Run(); err != nil {
		return fmt.Errorf("update-index: %w", err)
	}

	// 3. Write the new tree.
	writeTree := exec.CommandContext(ctx, "git", "write-tree")
	writeTree.Env = append(gitEnv, "GIT_INDEX_FILE="+tmpIndex)
	writeTree.Stderr = io.Discard
	treeHashBytes, err := writeTree.Output()
	if err != nil {
		return fmt.Errorf("write-tree: %w", err)
	}
	treeHash := strings.TrimSpace(string(treeHashBytes))

	// 4. Get the parent commit SHA.
	revParse := exec.CommandContext(ctx, "git", "rev-parse", ref)
	revParse.Env = gitEnv
	revParse.Stderr = io.Discard
	parentBytes, err := revParse.Output()
	if err != nil {
		return fmt.Errorf("rev-parse: %w", err)
	}
	parent := strings.TrimSpace(string(parentBytes))

	// 5. Create the commit.
	commitMsg := "Update automation config\n"
	commitTree := exec.CommandContext(ctx, "git", "commit-tree", treeHash,
		"-p", parent, "-m", commitMsg)
	commitTree.Env = append(gitEnv,
		"GIT_AUTHOR_NAME="+authorName,
		"GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME="+authorName,
		"GIT_COMMITTER_EMAIL="+authorEmail,
	)
	commitTree.Stderr = io.Discard
	commitHashBytes, err := commitTree.Output()
	if err != nil {
		return fmt.Errorf("commit-tree: %w", err)
	}
	commitHash := strings.TrimSpace(string(commitHashBytes))

	// 6. Update the ref.
	updateRef := exec.CommandContext(ctx, "git", "update-ref", ref, commitHash)
	updateRef.Env = gitEnv
	updateRef.Stderr = io.Discard
	if err := updateRef.Run(); err != nil {
		return fmt.Errorf("update-ref: %w", err)
	}

	return nil
}

// compile-time check
var _ server.RouteProvider = (*Handler)(nil)
