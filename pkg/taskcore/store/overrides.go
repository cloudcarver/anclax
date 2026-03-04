package store

import (
	"time"

	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/pkg/errors"
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

func WithParentTaskID(parentTaskID int32) TaskOverride {
	return func(task *apigen.Task) error {
		task.ParentTaskId = &parentTaskID
		return nil
	}
}

func WithLabels(labels []string) TaskOverride {
	return func(task *apigen.Task) error {
		labelsCopy := append([]string(nil), labels...)
		task.Attributes.Labels = &labelsCopy
		return nil
	}
}

func WithSerialKey(serialKey string) TaskOverride {
	return func(task *apigen.Task) error {
		task.Attributes.SerialKey = &serialKey
		return nil
	}
}

func WithSerialID(serialID int32) TaskOverride {
	return func(task *apigen.Task) error {
		task.Attributes.SerialID = &serialID
		return nil
	}
}

func WithPriority(priority int32) TaskOverride {
	return func(task *apigen.Task) error {
		if priority < 0 {
			return errors.New("priority must be non-negative")
		}
		task.Attributes.Priority = &priority
		return nil
	}
}

func WithWeight(weight int32) TaskOverride {
	return func(task *apigen.Task) error {
		if weight < 1 {
			return errors.New("weight must be greater than or equal to 1")
		}
		task.Attributes.Weight = &weight
		return nil
	}
}
