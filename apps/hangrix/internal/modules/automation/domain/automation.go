// Package domain declares the automation module's types and interfaces.
// Other modules depend only on this package; the Postgres implementation
// and HTTP handler live in sibling packages.
package domain

import (
	"context"
	"time"
)

// Status is the lifecycle state of a single automation run.
type Status string

const (
	StatusRunning Status = "running"
	StatusSuccess Status = "success"
	StatusFailed  Status = "failed"
)

// AutomationRun records a single execution of an automation task.
type AutomationRun struct {
	ID           int64
	RepoID       int64
	TaskName     string
	IssueID      *int64 // nil when the run failed before issue creation
	Status       Status
	ErrorMessage *string
	StartedAt    time.Time
	FinishedAt   *time.Time
	CreatedAt    time.Time
}

// Store is the persistence abstraction for automation_runs.
type Store interface {
	// CreateRun inserts a new automation_runs row in 'running' status.
	CreateRun(ctx context.Context, repoID int64, taskName string) (*AutomationRun, error)

	// CompleteRun updates the run to success with the created issue ID.
	CompleteRun(ctx context.Context, id int64, issueID int64) error

	// FailRun updates the run to failed with an error message.
	FailRun(ctx context.Context, id int64, errMsg string) error

	// LastSuccessfulRun returns the most recent successful run for a
	// (repo, task) pair, or nil if none exists.
	LastSuccessfulRun(ctx context.Context, repoID int64, taskName string) (*AutomationRun, error)

	// RecentRunExists returns true if a run for (repo, task) was created
	// within the given duration. Used for deduplication.
	RecentRunExists(ctx context.Context, repoID int64, taskName string, within time.Duration) (bool, error)

	// ListRuns returns the most recent runs for a repo, optionally
	// filtered by task name.
	ListRuns(ctx context.Context, repoID int64, taskName string, limit int32) ([]*AutomationRun, error)
}

// RepoRef is a lightweight snapshot of a repo row used by the scheduler
// to decide whether a repo has automation tasks to execute.
type RepoRef struct {
	ID            int64
	Name          string
	DefaultBranch string
	OwnerName     string
	OwnerKind     string // "user" or "org"
	OwnerID       int64
	AuthorUserID  int64 // the user ID to use as the issue author
}

// RepoLister returns every repo in the system. Used by the scheduler
// to scan for automation configs.
type RepoLister interface {
	ListAll(ctx context.Context) ([]RepoRef, error)
}
