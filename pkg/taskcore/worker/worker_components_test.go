package worker

import (
	"testing"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestBuildWorkerComponentsAddsReservedWorkerLabel(t *testing.T) {
	workerID := "123e4567-e89b-12d3-a456-426614174000"
	cfg := &config.Config{}
	cfg.Worker.WorkerID = &workerID
	cfg.Worker.Labels = []string{"ops"}

	components, err := BuildWorkerComponents(cfg, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, components)
	labels := components.Engine.Labels()
	require.Contains(t, labels, "ops")
	require.Contains(t, labels, "worker:"+workerID)
}

func TestBuildWorkerComponentsNilConfigError(t *testing.T) {
	components, err := BuildWorkerComponents(nil, nil, nil)
	require.Error(t, err)
	require.Nil(t, components)
}

func TestBuildWorkerComponentsInvalidWorkerID(t *testing.T) {
	bad := "not-a-uuid"
	cfg := &config.Config{}
	cfg.Worker.WorkerID = &bad

	components, err := BuildWorkerComponents(cfg, nil, nil)
	require.Error(t, err)
	require.Nil(t, components)
}

func TestConfiguredWorkerLabelsAvoidsDuplicateReservedLabel(t *testing.T) {
	workerID := "123e4567-e89b-12d3-a456-426614174000"
	in := []string{"ops", "worker:" + workerID}
	out := configuredWorkerLabels(in, workerID)

	count := 0
	for _, label := range out {
		if label == "worker:"+workerID {
			count++
		}
	}
	require.Equal(t, 1, count)
}
