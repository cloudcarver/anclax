package worker

import (
	"errors"
	"fmt"
	"time"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/robfig/cron/v3"
)

// attemptOutcome is a pure decision. Persistence must additionally check the
// lease version and preserve control-plane transitions committed in the meantime.
type attemptOutcome struct {
	status         apigen.TaskStatus
	startedAt      *time.Time
	attempts       int32
	errorEvent     bool
	completedEvent bool
	failureHook    bool
}

func decideAttempt(task Task, execErr error, now time.Time) (attemptOutcome, error) {
	out := attemptOutcome{status: apigen.Completed, attempts: task.Attempts, completedEvent: true}
	if errors.Is(execErr, taskcore.ErrTaskLockLost) {
		return out, taskcore.ErrTaskLockLost
	}
	if status := failureStatusOverride(execErr); status != "" {
		out.status, out.completedEvent = status, false
		return out, nil
	}
	var deferred *taskcore.TaskDeferred
	if errors.As(execErr, &deferred) || errors.Is(execErr, taskcore.ErrTaskInterrupted) {
		delay := time.Duration(0)
		if deferred != nil {
			delay = deferred.Delay
		}
		if delay < 0 {
			return out, fmt.Errorf("task deferral delay must be non-negative")
		}
		next := now.Add(delay)
		out.status, out.startedAt, out.completedEvent = apigen.Pending, &next, false
		out.attempts = max(0, task.Attempts-1)
		return out, nil
	}
	if execErr != nil {
		out.completedEvent = false
		out.errorEvent = !errors.Is(execErr, taskcore.ErrRetryTaskWithoutErrorEvent)
		if shouldRetryTask(execErr, task.Attributes.RetryPolicy, task.Attempts) {
			next, err := nextRetryTime(task.Attributes.RetryPolicy.Interval, now)
			if err != nil {
				return out, err
			}
			out.status, out.startedAt = apigen.Pending, &next
			return out, nil
		}
		out.status, out.failureHook, out.errorEvent = apigen.Failed, true, true
	}
	if task.Attributes.Cronjob != nil {
		next, err := nextCronTime(task.Attributes.Cronjob.CronExpression, now)
		if err != nil {
			return out, err
		}
		out.status, out.startedAt, out.attempts, out.completedEvent = apigen.Pending, &next, 0, false
	}
	return out, nil
}

func failureStatusOverride(execErr error) apigen.TaskStatus {
	switch {
	case errors.Is(execErr, taskcore.ErrTaskCancelled):
		return apigen.Cancelled
	case errors.Is(execErr, taskcore.ErrTaskPaused):
		return apigen.Paused
	default:
		return ""
	}
}

func shouldRetryTask(execErr error, policy *apigen.TaskRetryPolicy, attempts int32) bool {
	return !errors.Is(execErr, taskcore.ErrFatalTask) && policy != nil && (policy.MaxAttempts < 0 || attempts < policy.MaxAttempts)
}

func nextRetryTime(interval string, now time.Time) (time.Time, error) {
	duration, err := time.ParseDuration(interval)
	if err != nil || duration <= 0 {
		return time.Time{}, fmt.Errorf("invalid retry policy interval %q", interval)
	}
	return now.Add(duration), nil
}

func nextCronTime(expr string, now time.Time) (time.Time, error) {
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression: %w", err)
	}
	next := schedule.Next(now)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression %q has no next occurrence", expr)
	}
	return next, nil
}
