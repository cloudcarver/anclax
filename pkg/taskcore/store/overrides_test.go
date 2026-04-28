package store

import (
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/stretchr/testify/require"
)

func TestWithLabelsOverride(t *testing.T) {
	task := &apigen.Task{
		Attributes: apigen.TaskAttributes{},
	}

	err := WithLabels([]string{"billing", "critical"})(task)
	require.NoError(t, err)
	require.NotNil(t, task.Attributes.Labels)
	require.Equal(t, []string{"billing", "critical"}, *task.Attributes.Labels)
}

func TestWithTagsOverride(t *testing.T) {
	task := &apigen.Task{
		Attributes: apigen.TaskAttributes{},
	}

	err := WithTags([]string{"org:acme", "billing"})(task)
	require.NoError(t, err)
	require.NotNil(t, task.Attributes.Tags)
	require.Equal(t, []string{"org:acme", "billing"}, *task.Attributes.Tags)
}

func TestWithSerialKeyOverride(t *testing.T) {
	task := &apigen.Task{
		Attributes: apigen.TaskAttributes{},
	}

	err := WithSerialKey("order-42")(task)
	require.NoError(t, err)
	require.NotNil(t, task.Attributes.SerialKey)
	require.Equal(t, "order-42", *task.Attributes.SerialKey)
}

func TestWithSerialIDOverride(t *testing.T) {
	task := &apigen.Task{
		Attributes: apigen.TaskAttributes{},
	}

	err := WithSerialID(7)(task)
	require.NoError(t, err)
	require.NotNil(t, task.Attributes.SerialID)
	require.Equal(t, int32(7), *task.Attributes.SerialID)
}

func TestWithPriorityOverride(t *testing.T) {
	task := &apigen.Task{
		Attributes: apigen.TaskAttributes{},
	}

	err := WithPriority(9)(task)
	require.NoError(t, err)
	require.NotNil(t, task.Attributes.Priority)
	require.Equal(t, int32(9), *task.Attributes.Priority)
}

func TestWithPriorityOverrideRejectsNegative(t *testing.T) {
	task := &apigen.Task{
		Attributes: apigen.TaskAttributes{},
	}

	err := WithPriority(-1)(task)
	require.Error(t, err)
}

func TestWithWeightOverride(t *testing.T) {
	task := &apigen.Task{
		Attributes: apigen.TaskAttributes{},
	}

	err := WithWeight(4)(task)
	require.NoError(t, err)
	require.NotNil(t, task.Attributes.Weight)
	require.Equal(t, int32(4), *task.Attributes.Weight)
}

func TestWithWeightOverrideRejectsNonPositive(t *testing.T) {
	task := &apigen.Task{
		Attributes: apigen.TaskAttributes{},
	}

	err := WithWeight(0)(task)
	require.Error(t, err)
}

func TestWithDelayOverride(t *testing.T) {
	startedAt := time.Date(2025, 4, 1, 10, 0, 0, 0, time.UTC)
	task := &apigen.Task{
		Attributes: apigen.TaskAttributes{},
		StartedAt:  &startedAt,
	}

	err := WithDelay(2 * time.Minute)(task)
	require.NoError(t, err)
	require.NotNil(t, task.StartedAt)
	require.Equal(t, startedAt.Add(2*time.Minute), *task.StartedAt)
}

func TestWithStartedAtOverride(t *testing.T) {
	startedAt := time.Date(2025, 4, 1, 11, 0, 0, 0, time.UTC)
	task := &apigen.Task{Attributes: apigen.TaskAttributes{}}

	err := WithStartedAt(startedAt)(task)
	require.NoError(t, err)
	require.NotNil(t, task.StartedAt)
	require.Equal(t, startedAt, *task.StartedAt)
}

func TestWithUniqueTagOverride(t *testing.T) {
	task := &apigen.Task{Attributes: apigen.TaskAttributes{}}

	err := WithUniqueTag("tag-42")(task)
	require.NoError(t, err)
	require.NotNil(t, task.UniqueTag)
	require.Equal(t, "tag-42", *task.UniqueTag)
}

func TestWithRetryPolicyOverride(t *testing.T) {
	task := &apigen.Task{Attributes: apigen.TaskAttributes{}}

	err := WithRetryPolicy("5s", 3)(task)
	require.NoError(t, err)
	require.NotNil(t, task.Attributes.RetryPolicy)
	require.Equal(t, "5s", task.Attributes.RetryPolicy.Interval)
	require.Equal(t, int32(3), task.Attributes.RetryPolicy.MaxAttempts)
}

func TestWithCronjobOverride(t *testing.T) {
	task := &apigen.Task{Attributes: apigen.TaskAttributes{}}

	err := WithCronjob("*/5 * * * * *")(task)
	require.NoError(t, err)
	require.NotNil(t, task.Attributes.Cronjob)
	require.Equal(t, "*/5 * * * * *", task.Attributes.Cronjob.CronExpression)
}
