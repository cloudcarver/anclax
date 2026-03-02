package ctrl

import (
	"context"
	"math"
	"testing"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRunUpdateWorkerRuntimeConfigTaskAddsMaxPriority(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	maxStrict := int32(80)
	req := &UpdateWorkerRuntimeConfigRequest{
		MaxStrictPercentage: &maxStrict,
		Labels:              []string{"billing"},
		Weights:             []int32{3},
	}

	mockRunner.EXPECT().RunUpdateWorkerRuntimeConfig(context.Background(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, params *taskgen.UpdateWorkerRuntimeConfigParameters, overrides ...func(*apigen.Task) error) (int32, error) {
			require.Equal(t, req.MaxStrictPercentage, params.MaxStrictPercentage)
			require.Equal(t, req.Labels, params.Labels)
			require.Equal(t, req.Weights, params.Weights)
			require.NotEmpty(t, overrides)
			task := &apigen.Task{Attributes: apigen.TaskAttributes{}}
			err := overrides[0](task)
			require.NoError(t, err)
			require.NotNil(t, task.Attributes.Priority)
			require.Equal(t, ConfigUpdateTaskPriority, *task.Attributes.Priority)
			return int32(99), nil
		},
	)

	taskID, err := RunUpdateWorkerRuntimeConfigTask(context.Background(), mockRunner, req)
	require.NoError(t, err)
	require.Equal(t, int32(99), taskID)
}

func TestRunUpdateWorkerRuntimeConfigTaskKeepsMaxPriorityWhenOverrideProvided(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	req := &UpdateWorkerRuntimeConfigRequest{}
	lowPriority := taskcore.WithPriority(1)

	mockRunner.EXPECT().RunUpdateWorkerRuntimeConfig(context.Background(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, params *taskgen.UpdateWorkerRuntimeConfigParameters, overrides ...func(*apigen.Task) error) (int32, error) {
			task := &apigen.Task{Attributes: apigen.TaskAttributes{}}
			require.Len(t, overrides, 2)
			require.NoError(t, overrides[0](task))
			require.NoError(t, overrides[1](task))
			require.NotNil(t, task.Attributes.Priority)
			require.Equal(t, ConfigUpdateTaskPriority, *task.Attributes.Priority)
			return int32(100), nil
		},
	)

	taskID, err := RunUpdateWorkerRuntimeConfigTask(context.Background(), mockRunner, req, lowPriority)
	require.NoError(t, err)
	require.Equal(t, int32(100), taskID)
}

func TestRunUpdateWorkerRuntimeConfigTaskPriorityMaxInt32Sanity(t *testing.T) {
	require.Equal(t, int32(math.MaxInt32), ConfigUpdateTaskPriority)
}
