package taskcore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
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
			Payload: json.RawMessage(`{}`),
		}
		currentTime      = time.Date(2025, 3, 31, 12, 0, 0, 0, time.UTC)
		expectedNextTime = time.Date(2025, 3, 31, 12, 0, 5, 0, time.UTC)
	)

	mockModel := model.NewMockModelInterface(ctrl)

	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(&querier.AnclaxTask{
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
	err := taskStore.UpdateCronJob(ctx, taskID, cronExpression, json.RawMessage(`{}`))
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

	err := taskStore.PauseTask(ctx, taskID)
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
	err := taskStore.ResumeTask(ctx, taskID)
	require.NoError(t, err)
}

func TestGetTaskByID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(1)

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(&querier.AnclaxTask{
		ID:         taskID,
		Attributes: apigen.TaskAttributes{},
		Spec:       apigen.TaskSpec{},
		Status:     string(apigen.Pending),
		Attempts:   2,
	}, nil)

	store := &TaskStore{model: mockModel}
	task, err := store.GetTaskByID(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, taskID, task.ID)
	require.Equal(t, apigen.Pending, task.Status)
}

func TestGetTaskByIDNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(1)

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(nil, pgx.ErrNoRows)

	store := &TaskStore{model: mockModel}
	_, err := store.GetTaskByID(ctx, taskID)
	require.ErrorIs(t, err, ErrTaskNotFound)
}

func TestGetLastTaskErrorEvent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	createdAt := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	taskID := int32(1)

	spec := apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  "boom",
		},
	}

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetLastTaskErrorEvent(ctx, taskID).Return(&querier.AnclaxEvent{
		ID:        10,
		Spec:      spec,
		CreatedAt: createdAt,
	}, nil)

	store := &TaskStore{model: mockModel}
	event, err := store.GetLastTaskErrorEvent(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, int32(10), event.ID)
	require.Equal(t, apigen.TaskError, event.Spec.Type)
	require.NotNil(t, event.Spec.TaskError)
	require.Equal(t, "boom", event.Spec.TaskError.Error)
}

func TestGetLastTaskErrorEventNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(1)

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetLastTaskErrorEvent(ctx, taskID).Return(nil, pgx.ErrNoRows)

	store := &TaskStore{model: mockModel}
	_, err := store.GetLastTaskErrorEvent(ctx, taskID)
	require.ErrorIs(t, err, ErrTaskEventNotFound)
}
