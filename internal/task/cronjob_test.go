package task

import (
	"context"
	"testing"
	"time"

	"github.com/cloudcarver/anchor/internal/apigen"
	"github.com/cloudcarver/anchor/internal/model"
	"github.com/cloudcarver/anchor/internal/model/querier"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestUpdateCronJob(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID           = int32(1)
		cronExpression   = "0 0 * * *"
		taskSpec         = apigen.TaskSpec{}
		currentTime      = time.Date(2025, 3, 31, 12, 0, 0, 0, time.UTC)
		expectedNextTime = time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	)

	mockModel := model.NewMockModelInterface(ctrl)

	c := &model.Context{
		ModelInterface: mockModel,
		Context:        context.Background(),
	}

	mockModel.EXPECT().GetTaskByID(c, taskID).Return(&apigen.Task{
		ID: taskID,
		Attributes: apigen.TaskAttributes{
			Cronjob: &apigen.TaskCronjob{
				CronExpression: cronExpression,
			},
		},
	}, nil)

	mockModel.EXPECT().UpdateTask(c, querier.UpdateTaskParams{
		ID: taskID,
		Attributes: apigen.TaskAttributes{
			Cronjob: &apigen.TaskCronjob{
				CronExpression: cronExpression,
			},
		},
		Spec:      taskSpec,
		StartedAt: &expectedNextTime,
	})

	taskStore := &TaskStore{
		now: func() time.Time {
			return currentTime
		},
	}
	err := taskStore.UpdateCronJob(c, taskID, cronExpression, []byte{})
	require.NoError(t, err)
}

func TestPauseCronJob(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID = int32(1)
	)

	mockModel := model.NewMockModelInterface(ctrl)

	c := &model.Context{
		ModelInterface: mockModel,
		Context:        context.Background(),
	}

	mockModel.EXPECT().UpdateTaskStatus(c, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Paused),
	}).Return(nil)

	taskStore := &TaskStore{}
	err := taskStore.PauseCronJob(c, taskID)
	require.NoError(t, err)
}

func TestResumeCronJob(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID = int32(1)
	)

	mockModel := model.NewMockModelInterface(ctrl)

	c := &model.Context{
		ModelInterface: mockModel,
		Context:        context.Background(),
	}

	mockModel.EXPECT().UpdateTaskStatus(c, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Pending),
	}).Return(nil)

	taskStore := &TaskStore{}
	err := taskStore.ResumeCronJob(c, taskID)
	require.NoError(t, err)
}
