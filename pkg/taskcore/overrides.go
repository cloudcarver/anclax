package taskcore

import (
	"time"

	"github.com/cloudcarver/anchor/pkg/utils"
	"github.com/cloudcarver/anchor/pkg/zgen/apigen"
)

func WithRetryPolicy(interval string, maxAttempts int32) TaskOverride {
	return func(task *apigen.Task) error {
		task.Attributes.RetryPolicy = &apigen.TaskRetryPolicy{Interval: interval, MaxAttempts: maxAttempts}
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

func WithUniqueTag(uniqueTag string) TaskOverride {
	return func(task *apigen.Task) error {
		task.UniqueTag = &uniqueTag
		return nil
	}
}
