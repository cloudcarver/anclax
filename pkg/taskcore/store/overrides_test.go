package store

import (
	"testing"

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
