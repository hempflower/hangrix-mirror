// Package service implements the automation module's business logic:
// cron validation, scheduling, and issue-creation execution.
package service

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
)

// Validator checks automation task definitions for correctness.
// It depends on robfig/cron for schedule parsing.
type Validator struct {
	parser cron.Parser
}

// NewValidator returns a ready-to-use Validator. Zero state.
func NewValidator() *Validator {
	return &Validator{
		parser: cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
	}
}

// ValidateTask checks a single task's schedule is parseable.
// Returns nil on success, or an error describing the problem.
func (v *Validator) ValidateTask(task *agentsconfig.Task) error {
	if task.Schedule == "" {
		return fmt.Errorf("schedule: required")
	}
	_, err := v.parser.Parse(task.Schedule)
	if err != nil {
		return fmt.Errorf("schedule %q: %w", task.Schedule, err)
	}
	return nil
}

// ValidateConfig checks every task in the config. Returns nil when all
// tasks pass, or an error listing every failure.
func (v *Validator) ValidateConfig(cfg *agentsconfig.AutomationConfig) error {
	var errs []string
	for i, t := range cfg.Tasks {
		if t == nil {
			errs = append(errs, fmt.Sprintf("tasks[%d]: is nil", i))
			continue
		}
		if err := v.ValidateTask(t); err != nil {
			errs = append(errs, fmt.Sprintf("tasks[%d] (%s): %v", i, t.Name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("cron validation: %v", errs)
	}
	return nil
}

// Parse returns the cron schedule for the given expression.
func (v *Validator) Parse(schedule string) (cron.Schedule, error) {
	return v.parser.Parse(schedule)
}

// Next returns the next scheduled time for a cron expression relative to now.
func (v *Validator) Next(schedule string) (time.Time, error) {
	sched, err := v.parser.Parse(schedule)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(time.Now()), nil
}
