package taskcore

import (
	"time"

	"github.com/cloudcarver/anchor/pkg/apigen"
	"github.com/cloudcarver/anchor/pkg/utils"
)

func WithRetryPolicy(interval string, alwaysRetryOnFailure bool) TaskOverride {
	return func(task *apigen.Task) error {
		task.Attributes.RetryPolicy = &apigen.TaskRetryPolicy{Interval: interval, AlwaysRetryOnFailure: alwaysRetryOnFailure}
		return nil
	}
}

func WithCronjob(cronExpression string) TaskOverride {
	return func(task *apigen.Task) error {
		task.Attributes.Cronjob = &apigen.TaskCronjob{CronExpression: cronExpression}
		return nil
	}
}

func WithDelay(delay time.Duration) TaskOverride {
	return func(task *apigen.Task) error {
		task.StartedAt = utils.Ptr(task.StartedAt.Add(delay))
		return nil
	}
}

func WithStartedAt(startedAt time.Time) TaskOverride {
	return func(task *apigen.Task) error {
		task.StartedAt = utils.Ptr(startedAt)
		return nil
	}
}
