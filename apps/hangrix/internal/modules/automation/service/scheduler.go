package service

import (
	"context"
	"io"
	"log"
	"os/exec"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
)

// scannerIntervalDefault is the fallback when config.Automation.ScannerInterval
// is zero or unset.
const scannerIntervalDefault = 60 * time.Second

// dedupWindow is the time window in which we suppress duplicate runs
// for the same (repo, task) pair.
const dedupWindow = 60 * time.Second

// taskKey uniquely identifies a task within a repo for the first-seen map.
type taskKey struct {
	repoID   int64
	taskName string
}

// Scheduler is a BackgroundJob that scans every repo on a ticker,
// reads .hangrix/automation.yml from the repo's default branch via
// git cat-file, and triggers enabled tasks whose cron schedule has
// elapsed since the last successful run.
type Scheduler struct {
	lister    domain.RepoLister
	pathRes   repodomain.PathResolver
	validator *Validator
	executor  *Executor
	interval  time.Duration
	// firstSeen records when a never-run task was first observed by the
	// scheduler. On subsequent scans this timestamp is used as the cron
	// reference so that new tasks auto-fire without manual kick-off.
	firstSeen map[taskKey]time.Time
}

// SchedulerDeps wires the Scheduler's dependencies through ioc.
type SchedulerDeps struct {
	Lister    domain.RepoLister
	PathRes   repodomain.PathResolver
	Validator *Validator
	Executor  *Executor
	Config    *config.Config
}

// NewScheduler returns a ready-to-use background Scheduler.
func NewScheduler(deps *SchedulerDeps) *Scheduler {
	interval := deps.Config.Automation.ScannerInterval
	if interval <= 0 {
		interval = scannerIntervalDefault
	}
	return &Scheduler{
		lister:    deps.Lister,
		pathRes:   deps.PathRes,
		validator: deps.Validator,
		executor:  deps.Executor,
		interval:  interval,
		firstSeen: make(map[taskKey]time.Time),
	}
}

// Start runs the scan loop on a ticker. It does one immediate scan on
// startup so a restart doesn't introduce a full-tick delay.
func (s *Scheduler) Start(ctx context.Context) {
	s.scanOnce(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.scanOnce(ctx)
		}
	}
}

// scanOnce lists all repos and processes each one.
func (s *Scheduler) scanOnce(ctx context.Context) {
	repos, err := s.lister.ListAll(ctx)
	if err != nil {
		log.Printf("automation scheduler: list repos: %v", err)
		return
	}
	for _, repo := range repos {
		s.processRepo(ctx, repo)
	}
}

// processRepo reads a single repo's automation config and triggers
// eligible tasks.
func (s *Scheduler) processRepo(ctx context.Context, repo domain.RepoRef) {
	fsPath, err := s.pathRes.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		// Bad owner/name combo — skip silently.
		return
	}

	// Read .hangrix/automation.yml from the repo's default branch.
	raw, ok := readBlob(ctx, fsPath, repo.DefaultBranch, ".hangrix/automation.yml")
	if !ok {
		// File doesn't exist or can't be read — skip.
		return
	}

	cfg, err := agentsconfig.ParseAutomationConfig(raw)
	if err != nil {
		log.Printf("automation scheduler: repo %d parse config: %v", repo.ID, err)
		return
	}
	if err := cfg.Validate(); err != nil {
		log.Printf("automation scheduler: repo %d validate config: %v", repo.ID, err)
		return
	}
	if err := s.validator.ValidateConfig(cfg); err != nil {
		log.Printf("automation scheduler: repo %d cron validation: %v", repo.ID, err)
		return
	}

	now := time.Now()
	for _, task := range cfg.Tasks {
		if task == nil || !task.Enabled {
			continue
		}
		s.processTask(ctx, repo, task, now)
	}
}

// processTask decides whether a single task should fire.
func (s *Scheduler) processTask(ctx context.Context, repo domain.RepoRef, task *agentsconfig.Task, now time.Time) {
	// Parse the cron schedule.
	sched, err := s.validator.Parse(task.Schedule)
	if err != nil {
		log.Printf("automation scheduler: repo %d task %s bad schedule: %v", repo.ID, task.Name, err)
		return
	}

	// Get the last successful run time.
	lastRun, err := s.executor.runs.LastSuccessfulRun(ctx, repo.ID, task.Name)
	if err != nil {
		log.Printf("automation scheduler: repo %d task %s last run lookup: %v", repo.ID, task.Name, err)
		return
	}

	// Determine the reference time for cron scheduling.
	// For never-run tasks we remember when we first saw them and use that
	// as the reference — this avoids a startup explosion while still
	// letting tasks auto-fire on their next cron tick (e.g. "* * * * *"
	// fires ~60s after the second scan).
	var refTime time.Time
	if lastRun == nil {
		key := taskKey{repoID: repo.ID, taskName: task.Name}
		if t, seen := s.firstSeen[key]; seen {
			refTime = t
		} else {
			s.firstSeen[key] = now
			return
		}
	} else {
		refTime = lastRun.CreatedAt
	}

	// Compute the next scheduled time after the reference time.
	nextTime := sched.Next(refTime)

	// If the next scheduled time hasn't happened yet, skip.
	if nextTime.After(now) {
		return
	}

	// Dedup: don't fire if we already created a run within the dedup window.
	exists, err := s.executor.runs.RecentRunExists(ctx, repo.ID, task.Name, dedupWindow)
	if err != nil {
		log.Printf("automation scheduler: repo %d task %s dedup check: %v", repo.ID, task.Name, err)
		return
	}
	if exists {
		return
	}

	// Fire.
	log.Printf("automation scheduler: triggering repo %d task %s (ref: %v, next: %v)",
		repo.ID, task.Name, refTime, nextTime)
	if _, err := s.executor.Execute(ctx, repo.ID, repo.DefaultBranch, repo.AuthorUserID, task); err != nil {
		log.Printf("automation scheduler: repo %d task %s execute: %v", repo.ID, task.Name, err)
	}
}

// readBlob reads a file at ref:path from a bare repo. Returns (content, true)
// on success, (nil, false) when the file doesn't exist or can't be read.
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

// compile-time check
var _ server.BackgroundJob = (*Scheduler)(nil)
