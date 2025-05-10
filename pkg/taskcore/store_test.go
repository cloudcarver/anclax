package taskcore

import (
	"context"
	"testing"
	"time"

	"github.com/cloudcarver/anchor/pkg/apigen"
	"github.com/cloudcarver/anchor/pkg/model"
	"github.com/cloudcarver/anchor/pkg/model/querier"
	"github.com/cloudcarver/anchor/pkg/utils"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestUpdateCronJob(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		ctx            = context.Background()
		taskID         = int32(1)
		cronExpression = "*/5 * * * * *"
		taskSpec       = apigen.TaskSpec{
			Payload: []byte{},
		}
		currentTime      = time.Date(2025, 3, 31, 12, 0, 0, 0, time.UTC)
		expectedNextTime = time.Date(2025, 3, 31, 12, 0, 5, 0, time.UTC)
	)

	mockModel := model.NewMockModelInterface(ctrl)

	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(&querier.Task{
		ID: taskID,
		Attributes: apigen.TaskAttributes{
			Cronjob: &apigen.TaskCronjob{
				CronExpression: cronExpression,
			},
		},
	}, nil)

	mockModel.EXPECT().UpdateTask(ctx, utils.NewJSONValueMatcher(t, querier.UpdateTaskParams{
		ID: taskID,
		Attributes: apigen.TaskAttributes{
			Cronjob: &apigen.TaskCronjob{
				CronExpression: cronExpression,
			},
		},
		Spec:      taskSpec,
		StartedAt: &expectedNextTime,
	}))

	taskStore := &TaskStore{
		model: mockModel,
		now: func() time.Time {
			return currentTime
		},
	}
	err := taskStore.UpdateCronJob(ctx, taskID, cronExpression, []byte{})
	require.NoError(t, err)
}

func TestPauseCronJob(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		ctx    = context.Background()
		taskID = int32(1)
	)

	mockModel := model.NewMockModelInterface(ctrl)

	mockModel.EXPECT().UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Paused),
	}).Return(nil)

	taskStore := &TaskStore{
		model: mockModel,
	}

	err := taskStore.PauseCronJob(ctx, taskID)
	require.NoError(t, err)
}

func TestResumeCronJob(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		ctx    = context.Background()
		taskID = int32(1)
	)

	mockModel := model.NewMockModelInterface(ctrl)

	mockModel.EXPECT().UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Pending),
	}).Return(nil)

	taskStore := &TaskStore{
		model: mockModel,
	}
	err := taskStore.ResumeCronJob(ctx, taskID)
	require.NoError(t, err)
}
