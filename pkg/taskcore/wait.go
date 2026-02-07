package taskcore

import (
	"context"
	"strconv"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/pkg/errors"
)

const defaultWaitForTaskPollInterval = 200 * time.Millisecond

type WaitForTaskOption func(*waitForTaskConfig)

type waitForTaskConfig struct {
	pollInterval time.Duration
	timeout      time.Duration
}

func WithWaitForTaskPollInterval(interval time.Duration) WaitForTaskOption {
	return func(cfg *waitForTaskConfig) {
		cfg.pollInterval = interval
	}
}

func WithWaitForTaskTimeout(timeout time.Duration) WaitForTaskOption {
	return func(cfg *waitForTaskConfig) {
		cfg.timeout = timeout
	}
}

func (s *TaskStore) WaitForTask(ctx context.Context, taskID int32, opts ...WaitForTaskOption) error {
	// WaitForTask blocks until the task reaches a terminal state or the context ends.
	// It polls the task status at the configured interval and respects an optional timeout.
	// On failure, it returns an error with attempts, max attempts, and the last error event message.
	cfg := waitForTaskConfig{pollInterval: defaultWaitForTaskPollInterval}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.pollInterval <= 0 {
		cfg.pollInterval = defaultWaitForTaskPollInterval
	}
	if cfg.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
	}

	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()

	for {
		task, err := s.GetTaskByID(ctx, taskID)
		if err != nil {
			return errors.Wrap(err, "get task")
		}
		switch task.Status {
		case apigen.Completed:
			return nil
		case apigen.Failed:
			return s.buildTaskFailedError(ctx, task)
		}

		select {
		case <-ctx.Done():
			return errors.Wrap(ctx.Err(), "wait for task")
		case <-ticker.C:
		}
	}
}

func (s *TaskStore) buildTaskFailedError(ctx context.Context, task *apigen.Task) error {
	lastError := "unknown"
	if event, err := s.GetLastTaskErrorEvent(ctx, task.ID); err == nil {
		if event.Spec.TaskError != nil {
			lastError = event.Spec.TaskError.Error
		}
	} else if !errors.Is(err, ErrTaskEventNotFound) {
		return errors.Wrap(err, "get last task error event")
	}

	maxAttempts := formatMaxAttempts(task.Attributes.RetryPolicy)
	return errors.Errorf(
		"task %d failed (attempts=%d max_attempts=%s last_error=%s)",
		task.ID,
		task.Attempts,
		maxAttempts,
		lastError,
	)
}

func formatMaxAttempts(policy *apigen.TaskRetryPolicy) string {
	if policy == nil {
		return "0"
	}
	if policy.MaxAttempts < 0 {
		return "unlimited"
	}
	return strconv.FormatInt(int64(policy.MaxAttempts), 10)
}
