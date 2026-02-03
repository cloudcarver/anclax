package taskcore

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
