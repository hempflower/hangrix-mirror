// Package service holds the workflow module's stateless business logic:
// config parsing, event matching, run creation, job advancement, and dispatch.
// It composes domain.Store with repo file readers and config parsers.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/workflowsconfig"
)

// Service is the stateless business-logic core for workflows. It satisfies
// domain.Dispatcher for cross-module runner integration.
type Service struct {
	store   domain.Store
	pathRes repodomain.PathResolver
}

// Deps wires the service's dependencies through ioc.
type Deps struct {
	Store   domain.Store
	PathRes repodomain.PathResolver
}

// New creates a ready-to-use workflow Service.
func New(deps *Deps) *Service {
	return &Service{
		store:   deps.Store,
		pathRes: deps.PathRes,
	}
}

// ---- config scanning ----

// ScanWorkflowConfigs reads all .hangrix/workflows/*.yml files from a repo's
// default branch and returns parsed+validated WorkflowConfigs.
func (s *Service) ScanWorkflowConfigs(ctx context.Context, repo Ref) ([]*workflowsconfig.WorkflowConfig, error) {
	fsPath, err := s.pathRes.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		return nil, fmt.Errorf("resolve repo path: %w", err)
	}

	// List files in .hangrix/workflows/
	files, err := listWorkflowFiles(ctx, fsPath, repo.DefaultBranch)
	if err != nil {
		// Missing directory is not an error — just no workflows
		return nil, nil
	}

	var configs []*workflowsconfig.WorkflowConfig
	for _, file := range files {
		raw, ok := readBlob(ctx, fsPath, repo.DefaultBranch, ".hangrix/workflows/"+file)
		if !ok {
			continue
		}
		cfg, err := workflowsconfig.ParseWorkflowConfig(raw, file)
		if err != nil {
			log.Printf("workflow: repo %d parse %s: %v", repo.ID, file, err)
			continue
		}
		configs = append(configs, cfg)
	}

	if err := workflowsconfig.ValidateConfigSet(configs); err != nil {
		log.Printf("workflow: repo %d validate config set: %v", repo.ID, err)
		// Still return what we could parse; individual files may be usable
	}

	return configs, nil
}

// GetHostContainer reads the host repo's .hangrix/agents.yml and extracts
// the container definition (image/build/entrypoint/volumes/env).
func (s *Service) GetHostContainer(ctx context.Context, repo Ref) (*agentsconfig.Container, error) {
	fsPath, err := s.pathRes.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		return nil, fmt.Errorf("resolve repo path: %w", err)
	}

	raw, ok := readBlob(ctx, fsPath, repo.DefaultBranch, ".hangrix/agents.yml")
	if !ok {
		return nil, fmt.Errorf("agents.yml not found in repo %s/%s", repo.OwnerName, repo.Name)
	}

	host, err := agentsconfig.ParseHostConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("parse agents.yml: %w", err)
	}

	// Container is a value type in HostConfig; an empty image means no container.
	if host.Container.Image == "" {
		return nil, fmt.Errorf("agents.yml has no container image defined")
	}

	return &host.Container, nil
}

// ---- event matching ----

// FindMatchingWorkflows returns the subset of workflow configs whose `on`
// triggers match the given event. For dispatch events, it returns the
// single named workflow.
func (s *Service) FindMatchingWorkflows(configs []*workflowsconfig.WorkflowConfig, event workflowsconfig.EventName, filter WorkflowEventFilter) []*workflowsconfig.WorkflowConfig {
	var matched []*workflowsconfig.WorkflowConfig
	for _, cfg := range configs {
		for _, trigger := range cfg.On {
			if trigger.Event != event {
				continue
			}
			switch event {
			case workflowsconfig.EventRepoPush:
				if trigger.MatchesPushEvent(filter.Branch, filter.ChangedPaths) {
					matched = append(matched, cfg)
				}
			case workflowsconfig.EventRepoPushTag:
				if trigger.MatchesPushTagEvent(filter.Tag) {
					matched = append(matched, cfg)
				}
			case workflowsconfig.EventIssueOpened:
				matched = append(matched, cfg)
			case workflowsconfig.EventIssueComment:
				if trigger.MatchesCommentEvent(filter.FromRole, filter.FromUser, filter.MentionedWorkflow) {
					matched = append(matched, cfg)
				}
			case workflowsconfig.EventWorkflowDispatch:
				if cfg.Name == filter.WorkflowName {
					matched = append(matched, cfg)
				}
			}
		}
	}
	return matched
}

// WorkflowEventFilter carries event-specific filtering criteria.
type WorkflowEventFilter struct {
	Branch            string
	ChangedPaths      []string
	Tag               string
	FromRole          string
	FromUser          string
	MentionedWorkflow string
	WorkflowName      string
}

// ---- run creation ----

// DispatchInput is a user-provided input for workflow.dispatch.
type DispatchInput struct {
	Name  string
	Value string
}

// CreateRunParams is the high-level input for creating a workflow run.
type CreateRunParams struct {
	Repo           Ref
	Config         *workflowsconfig.WorkflowConfig
	EventName      workflowsconfig.EventName
	CauseID        *int64
	Ref            string
	CommitSHA      string
	Container      *agentsconfig.Container
	DispatchInputs []DispatchInput
}

// CreateRun creates a new workflow run and all associated pending job runs.
// It merges environment variables from container ← workflow ← job levels,
// snapshots the container definition, and persists everything.
func (s *Service) CreateRun(ctx context.Context, params CreateRunParams) (*domain.WorkflowRun, []*domain.WorkflowJobRun, error) {
	// Build container snapshot
	containerEnv := make(map[string]string)
	if params.Container != nil {
		for k, v := range params.Container.Env {
			containerEnv[k] = v
		}
	}
	// Merge workflow-level env over container
	for k, v := range params.Config.Env {
		containerEnv[k] = v
	}

	// Build dispatch inputs as WORKFLOW_INPUT_UPPER_SNAKE
	dispatchInputs := make(map[string]string)
	for _, in := range params.DispatchInputs {
		key := "WORKFLOW_INPUT_" + strings.ToUpper(in.Name)
		dispatchInputs[key] = in.Value
	}

	// Build job defs
	jobDefs := make([]domain.JobDefInput, len(params.Config.Jobs))
	for i, job := range params.Config.Jobs {
		steps := make([]domain.StepInput, len(job.Steps))
		for si, step := range job.Steps {
			steps[si] = domain.StepInput{
				Name: step.Name,
				Run:  step.Run,
			}
		}
		jobDefs[i] = domain.JobDefInput{
			JobKey:           job.Key,
			DisplayName:      job.DisplayName,
			Env:              job.Env,
			TimeoutMinutes:   int32(job.TimeoutMinutes),
			WorkingDirectory: job.WorkingDirectory,
			Steps:            steps,
		}
	}

	// Snapshot container spec
	var snapImage string
	var snapBuild *domain.BuildSpec
	var snapEntrypoint []string
	var snapVolumes []domain.VolumeSnapshot
	if params.Container != nil {
		snapImage = params.Container.Image
		snapEntrypoint = params.Container.Entrypoint
		if params.Container.Build != nil {
			snapBuild = &domain.BuildSpec{
				Dockerfile: params.Container.Build.Dockerfile,
				Context:    params.Container.Build.Context,
				Args:       params.Container.Build.Args,
			}
		}
		for _, vol := range params.Container.Volumes {
			snapVolumes = append(snapVolumes, domain.VolumeSnapshot{
				Name:  vol.Name,
				Mount: vol.Mount,
			})
		}
	}

	return s.store.CreateRun(ctx, domain.CreateRunParams{
		RepoID:              params.Repo.ID,
		WorkflowName:        params.Config.Name,
		SourceFile:          params.Config.SourceFile,
		EventName:           domain.EventName(params.EventName),
		CauseID:             params.CauseID,
		Ref:                 params.Ref,
		CommitSHA:           params.CommitSHA,
		ContainerEnv:        containerEnv,
		ContainerImage:      snapImage,
		ContainerBuild:      snapBuild,
		ContainerEntrypoint: snapEntrypoint,
		ContainerVolumes:    snapVolumes,
		JobDefs:             jobDefs,
		DispatchInputs:      dispatchInputs,
	})
}

// ---- job advancement ----

// AdvanceRun is called after a job transitions to a terminal status.
// It either advances to the next pending job or marks the run terminal.
func (s *Service) AdvanceRun(ctx context.Context, runID int64) error {
	jobs, err := s.store.ListJobRunsByRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("advance run: list jobs: %w", err)
	}

	// Check if any job failed
	allDone := true
	anyFailed := false
	var failedJobSeq int32

	for _, job := range jobs {
		switch job.Status {
		case domain.JobStatusFailed:
			anyFailed = true
			allDone = true // stop on first failure
			failedJobSeq = job.SequenceIndex
		case domain.JobStatusPending, domain.JobStatusRunning:
			allDone = false
		}
	}

	if anyFailed {
		// Skip remaining pending jobs
		if err := s.store.SkipRemainingJobs(ctx, runID, failedJobSeq); err != nil {
			return fmt.Errorf("advance run: skip remaining: %w", err)
		}
		return s.store.MarkRunTerminal(ctx, runID, domain.RunStatusFailed)
	}

	if allDone {
		// All jobs succeeded
		return s.store.MarkRunTerminal(ctx, runID, domain.RunStatusSuccess)
	}

	// Otherwise, the next pending job will be picked up by a runner
	return nil
}

// ---- dispatcher (domain.Dispatcher) ----

// ClaimNextJob claims the next pending workflow job for a runner.
func (s *Service) ClaimNextJob(ctx context.Context, runnerID int64) (*domain.WorkflowJobRun, error) {
	job, err := s.store.ClaimNextJob(ctx, runnerID)
	if err != nil {
		return nil, err
	}

	// Mark the parent run as running if it's still pending
	parentRun, err := s.store.GetRun(ctx, job.WorkflowRunID)
	if err != nil {
		return nil, fmt.Errorf("claim next job: get run: %w", err)
	}
	if parentRun.Status == domain.RunStatusPending {
		if err := s.store.MarkRunStarted(ctx, parentRun.ID); err != nil {
			// Non-fatal: the run will be marked running on the next job claim
			log.Printf("workflow: mark run %d started: %v", parentRun.ID, err)
		}
	}

	return job, nil
}

// GetRunForJob returns the workflow run that owns the given job.
func (s *Service) GetRunForJob(ctx context.Context, jobRunID int64) (*domain.WorkflowRun, error) {
	job, err := s.store.GetJobRun(ctx, jobRunID)
	if err != nil {
		return nil, err
	}
	return s.store.GetRun(ctx, job.WorkflowRunID)
}


// ---- event triggers ----

// TriggerTagEvent implements domain.TagEventTrigger. It scans workflow
// configs, finds those matching repo.push_tag with the given tag name,
// and creates a workflow run for each match. This is the single entry
// point used by both the git-push PushObserver and the REST create-tag
// handler.
func (s *Service) TriggerTagEvent(ctx context.Context, repoID int64, ownerName, repoName, defaultBranch, tagName, commitSHA string) error {
	ref := Ref{
		ID:            repoID,
		Name:          repoName,
		DefaultBranch: defaultBranch,
		OwnerName:     ownerName,
	}

	configs, err := s.ScanWorkflowConfigs(ctx, ref)
	if err != nil {
		return fmt.Errorf("trigger tag event: scan configs: %w", err)
	}
	if len(configs) == 0 {
		return nil
	}

	matched := s.FindMatchingWorkflows(configs, workflowsconfig.EventRepoPushTag, WorkflowEventFilter{Tag: tagName})
	if len(matched) == 0 {
		return nil
	}

	container, err := s.GetHostContainer(ctx, ref)
	if err != nil {
		return fmt.Errorf("trigger tag event: get container: %w", err)
	}

	tagRef := "refs/tags/" + tagName
	for _, cfg := range matched {
		if _, _, err := s.CreateRun(ctx, CreateRunParams{
			Repo:      ref,
			Config:    cfg,
			EventName: workflowsconfig.EventRepoPushTag,
			Ref:       tagRef,
			CommitSHA: commitSHA,
			Container: container,
		}); err != nil {
			log.Printf("workflow: trigger tag event: create run for %s: %v", cfg.Name, err)
		}
	}
	return nil
}


// ---- dispatch ----

// Dispatch creates a new workflow run from a manual dispatch request.
func (s *Service) Dispatch(ctx context.Context, repo Ref, workflowName string, inputs []DispatchInput, ref string) (*domain.WorkflowRun, []*domain.WorkflowJobRun, error) {
	// Scan configs to find the named workflow
	configs, err := s.ScanWorkflowConfigs(ctx, repo)
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch: scan configs: %w", err)
	}

	var cfg *workflowsconfig.WorkflowConfig
	for _, c := range configs {
		if c.Name == workflowName {
			cfg = c
			break
		}
	}
	if cfg == nil {
		return nil, nil, fmt.Errorf("workflow %q not found in repo", workflowName)
	}

	// Check that workflow.dispatch is declared
	hasDispatch := false
	for _, t := range cfg.On {
		if t.Event == workflowsconfig.EventWorkflowDispatch {
			hasDispatch = true
			break
		}
	}
	if !hasDispatch {
		return nil, nil, fmt.Errorf("workflow %q does not accept dispatch triggers", workflowName)
	}

	// Validate inputs against declared dispatch inputs
	declared := make(map[string]*workflowsconfig.DispatchInput)
	for i := range cfg.DispatchInputs {
		declared[cfg.DispatchInputs[i].Name] = &cfg.DispatchInputs[i]
	}
	for _, in := range inputs {
		di, ok := declared[in.Name]
		if !ok {
			return nil, nil, fmt.Errorf("unknown dispatch input %q", in.Name)
		}
		_ = di // mark used
	}
	// Check required inputs are present
	for _, di := range cfg.DispatchInputs {
		if !di.Required {
			continue
		}
		found := false
		for _, in := range inputs {
			if in.Name == di.Name {
				found = true
				break
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("required dispatch input %q not provided", di.Name)
		}
	}

	// Resolve ref
	commitSHA := ref
	if ref == "" {
		// Use default branch latest commit
		fsPath, err := s.pathRes.ResolvePath(repo.OwnerName, repo.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("dispatch: resolve path: %w", err)
		}
		sha, err := resolveRef(ctx, fsPath, repo.DefaultBranch)
		if err != nil {
			return nil, nil, fmt.Errorf("dispatch: resolve default branch: %w", err)
		}
		commitSHA = sha
		ref = repo.DefaultBranch
	}

	// Get container from agents.yml
	container, err := s.GetHostContainer(ctx, repo)
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch: get container: %w", err)
	}

	return s.CreateRun(ctx, CreateRunParams{
		Repo:           repo,
		Config:         cfg,
		EventName:      workflowsconfig.EventWorkflowDispatch,
		Ref:            ref,
		CommitSHA:      commitSHA,
		Container:      container,
		DispatchInputs: inputs,
	})
}

// ---- helpers ----

// Ref is a lightweight reference to a repo, used by the service.
type Ref struct {
	ID            int64
	Name          string
	DefaultBranch string
	OwnerName     string
}

// listWorkflowFiles returns the names of all .yml/.yaml files in
// .hangrix/workflows/ on the given branch.
func listWorkflowFiles(ctx context.Context, fsPath, ref string) ([]string, error) {
	cmd := exec.CommandContext(ctx,
		"git",
		"--git-dir="+fsPath,
		"ls-tree",
		"--name-only",
		ref+":.hangrix/workflows",
	)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, ".yml") || strings.HasSuffix(line, ".yaml") {
			files = append(files, line)
		}
	}
	return files, nil
}

// readBlob reads a file at ref:path from a bare repo.
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

// resolveRef resolves a branch or tag name to a commit SHA.
func resolveRef(ctx context.Context, fsPath, ref string) (string, error) {
	cmd := exec.CommandContext(ctx,
		"git",
		"--git-dir="+fsPath,
		"rev-parse",
		ref,
	)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ---- store pass-through methods (for handler access) ----

// GetRun returns a workflow run by ID.
func (s *Service) GetRun(ctx context.Context, id int64) (*domain.WorkflowRun, error) {
	return s.store.GetRun(ctx, id)
}

// ListJobRuns returns all job runs for a workflow run.
func (s *Service) ListJobRuns(ctx context.Context, workflowRunID int64) ([]*domain.WorkflowJobRun, error) {
	return s.store.ListJobRunsByRun(ctx, workflowRunID)
}

// ListLogs returns log lines for a job run.
func (s *Service) ListLogs(ctx context.Context, jobRunID int64, offset, limit int32) ([]*domain.WorkflowJobLogLine, int64, error) {
	return s.store.ListLogs(ctx, jobRunID, offset, limit)
}

// MarkJobRunning transitions a job to running.
func (s *Service) MarkJobRunning(ctx context.Context, jobID, runnerID int64) error {
	return s.store.MarkJobRunning(ctx, jobID, runnerID)
}

// MarkJobTerminal transitions a job to a terminal status.
func (s *Service) MarkJobTerminal(ctx context.Context, jobID int64, status domain.JobStatus, exitCode *int32, errMsg string) error {
	return s.store.MarkJobTerminal(ctx, jobID, status, exitCode, errMsg)
}

// AppendLog appends a log line to a job run.
func (s *Service) AppendLog(ctx context.Context, jobRunID int64, stream domain.LogStream, line string) error {
	return s.store.AppendLog(ctx, jobRunID, stream, line)
}

// GetJobRun returns a job run by ID.
func (s *Service) GetJobRun(ctx context.Context, id int64) (*domain.WorkflowJobRun, error) {
	return s.store.GetJobRun(ctx, id)
}

// ListRunsByRepo returns paginated workflow runs for a repo.
func (s *Service) ListRunsByRepo(ctx context.Context, repoID int64, workflowName string, offset, limit int32) ([]*domain.WorkflowRun, int64, error) {
	return s.store.ListRunsByRepo(ctx, repoID, workflowName, offset, limit)
}


// Ensure json import is used
var _ = json.Marshal
