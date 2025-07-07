package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudcarver/anchor/pkg/taskcore"
	"github.com/cloudcarver/anchor/pkg/utils"
	"github.com/cloudcarver/anchor/pkg/zcore/model"
	"github.com/cloudcarver/anchor/pkg/zgen/apigen"
	"github.com/cloudcarver/anchor/pkg/zgen/querier"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestHandleCronjob(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	tz := "Asia/Shanghai"
	location, err := time.LoadLocation(tz)
	require.NoError(t, err)

	var (
		currTime = time.Date(2025, 3, 27, 0, 0, 1, 0, location)
		cronExpr = "0 0 0 * * *"
		nextTime = time.Date(2025, 3, 28, 0, 0, 0, 0, location)
		taskID   = int32(1)
	)

	handler := &TaskLifeCycleHandler{
		model: mockModel,
		taskHandler: mockTaskHandler,
		now: func() time.Time {
			return currTime
		},
	}

	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)
	mockTxm.EXPECT().UpdateTaskStartedAt(context.Background(), querier.UpdateTaskStartedAtParams{
		ID:        taskID,
		StartedAt: utils.Ptr(nextTime),
	}).Return(nil)

	task := apigen.Task{
		ID: taskID,
		Attributes: apigen.TaskAttributes{
			Cronjob: &apigen.TaskCronjob{
				CronExpression: cronExpr,
			},
		},
	}

	err = handler.handleCronjob(context.Background(), nil, task)
	require.NoError(t, err)
}

func TestHandleCompleted(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID = int32(1)
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model: mockModel,
		taskHandler: mockTaskHandler,
	}

	task := apigen.Task{
		ID: taskID,
	}

	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskCompleted,
		TaskCompleted: &apigen.EventTaskCompleted{
			TaskID: taskID,
		},
	}).Return(&querier.AnchorEvent{}, nil)

	mockTxm.EXPECT().UpdateTaskStatus(context.Background(), querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Completed),
	}).Return(nil)

	err := handler.HandleCompleted(context.Background(), nil, task)
	require.NoError(t, err)
}

func TestHandleFailed(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID = int32(1)
		err    = errors.New("test error")
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model: mockModel,
		taskHandler: mockTaskHandler,
	}

	task := apigen.Task{
		ID: taskID,
		Spec: apigen.TaskSpec{
			Type: "testTask",
		},
	}

	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  err.Error(),
		},
	}).Return(&querier.AnchorEvent{}, nil)

	mockTaskHandler.EXPECT().OnTaskFailed(context.Background(), gomock.Any(), &task.Spec, taskID).Return(nil)

	mockTxm.EXPECT().UpdateTaskStatus(context.Background(), querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Failed),
	}).Return(nil)

	err = handler.HandleFailed(context.Background(), nil, task, err)
	require.NoError(t, err)
}

func TestHandleFailedWithRetryPolicy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID      = int32(1)
		err         = errors.New("test error")
		intervalRaw = "1h"
		interval, _ = time.ParseDuration(intervalRaw)
		currTime    = time.Now()
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model: mockModel,
		taskHandler: mockTaskHandler,
		now: func() time.Time {
			return currTime
		},
	}

	task := apigen.Task{
		ID: taskID,
		Spec: apigen.TaskSpec{
			Type: "testTask",
		},
		Attributes: apigen.TaskAttributes{
			RetryPolicy: &apigen.TaskRetryPolicy{
				MaxAttempts: -1,
				Interval:    intervalRaw,
			},
		},
	}

	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  err.Error(),
		},
	}).Return(&querier.AnchorEvent{}, nil)

	mockTxm.EXPECT().UpdateTaskStartedAt(context.Background(), querier.UpdateTaskStartedAtParams{
		ID:        taskID,
		StartedAt: utils.Ptr(currTime.Add(interval)),
	}).Return(nil)

	// Note: OnTaskFailed is not called when task is retried

	err = handler.HandleFailed(context.Background(), nil, task, err)
	require.NoError(t, err)

}

func TestHandleFailed_ErrRetryTaskWithoutErrorEvent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID   = int32(1)
		currTime = time.Now()
		interval = 1 * time.Hour
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model: mockModel,
		taskHandler: mockTaskHandler,
		now: func() time.Time {
			return currTime
		},
	}

	task := apigen.Task{
		ID: taskID,
		Spec: apigen.TaskSpec{
			Type: "testTask",
		},
		Attributes: apigen.TaskAttributes{
			RetryPolicy: &apigen.TaskRetryPolicy{
				MaxAttempts: -1,
				Interval:    "1h",
			},
		},
	}

	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)
	mockTxm.EXPECT().UpdateTaskStartedAt(context.Background(), querier.UpdateTaskStartedAtParams{
		ID:        taskID,
		StartedAt: utils.Ptr(currTime.Add(interval)),
	}).Return(nil)

	// Note: OnTaskFailed is not called when task is retried

	err := handler.HandleFailed(context.Background(), nil, task, taskcore.ErrRetryTaskWithoutErrorEvent)
	require.NoError(t, err)
}

func TestHandleFailed_ErrFatalTask(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID   = int32(1)
		currTime = time.Now()
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model: mockModel,
		taskHandler: mockTaskHandler,
		now: func() time.Time {
			return currTime
		},
	}

	task := apigen.Task{
		ID: taskID,
		Spec: apigen.TaskSpec{
			Type: "testTask",
		},
		Attributes: apigen.TaskAttributes{
			RetryPolicy: &apigen.TaskRetryPolicy{
				MaxAttempts: -1,
				Interval:    "1h",
			},
		},
	}

	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  taskcore.ErrFatalTask.Error(),
		},
	}).Return(&querier.AnchorEvent{}, nil)

	mockTaskHandler.EXPECT().OnTaskFailed(context.Background(), gomock.Any(), &task.Spec, taskID).Return(nil)

	mockTxm.EXPECT().UpdateTaskStatus(context.Background(), querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Failed),
	}).Return(nil)

	err := handler.HandleFailed(context.Background(), nil, task, taskcore.ErrFatalTask)
	require.NoError(t, err)
}

func TestHandleFailed_ErrFatalTask_Cronjob(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID = int32(1)
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model: mockModel,
		taskHandler: mockTaskHandler,
	}

	task := apigen.Task{
		ID: taskID,
		Spec: apigen.TaskSpec{
			Type: "testTask",
		},
		Attributes: apigen.TaskAttributes{
			Cronjob: &apigen.TaskCronjob{
				CronExpression: "0 0 0 * * *",
			},
		},
	}

	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)
	// Expect that error event is still inserted for fatal errors
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  taskcore.ErrFatalTask.Error(),
		},
	}).Return(&querier.AnchorEvent{}, nil)

	// Note: For cronjobs, OnTaskFailed and UpdateTaskStatus should NOT be called
	// because cronjobs are designed to run again regardless of failures

	err := handler.HandleFailed(context.Background(), nil, task, taskcore.ErrFatalTask)
	require.NoError(t, err)
}

func TestHandleFailedWithMaxAttempts(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID       = int32(1)
		err          = errors.New("test error")
		maxAttempts  = int32(3)
		currAttempts = int32(2)
		intervalRaw  = "1h"
		interval, _  = time.ParseDuration(intervalRaw)
		currTime     = time.Now()
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model: mockModel,
		taskHandler: mockTaskHandler,
		now: func() time.Time {
			return currTime
		},
	}

	task := apigen.Task{
		ID:       taskID,
		Attempts: currAttempts,
		Spec: apigen.TaskSpec{
			Type: "testTask",
		},
		Attributes: apigen.TaskAttributes{
			RetryPolicy: &apigen.TaskRetryPolicy{
				MaxAttempts: maxAttempts,
				Interval:    intervalRaw,
			},
		},
	}

	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  err.Error(),
		},
	}).Return(&querier.AnchorEvent{}, nil)

	mockTxm.EXPECT().UpdateTaskStartedAt(context.Background(), querier.UpdateTaskStartedAtParams{
		ID:        taskID,
		StartedAt: utils.Ptr(currTime.Add(interval)),
	}).Return(nil)

	err = handler.HandleFailed(context.Background(), nil, task, err)
	require.NoError(t, err)
}

func TestHandleFailedExceedsMaxAttempts(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID       = int32(1)
		err          = errors.New("test error")
		maxAttempts  = int32(3)
		currAttempts = int32(3)
		intervalRaw  = "1h"
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model: mockModel,
		taskHandler: mockTaskHandler,
	}

	task := apigen.Task{
		ID:       taskID,
		Attempts: currAttempts,
		Spec: apigen.TaskSpec{
			Type: "testTask",
		},
		Attributes: apigen.TaskAttributes{
			RetryPolicy: &apigen.TaskRetryPolicy{
				MaxAttempts: maxAttempts,
				Interval:    intervalRaw,
			},
		},
	}

	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  err.Error(),
		},
	}).Return(&querier.AnchorEvent{}, nil)

	mockTaskHandler.EXPECT().OnTaskFailed(context.Background(), gomock.Any(), &task.Spec, taskID).Return(nil)

	mockTxm.EXPECT().UpdateTaskStatus(context.Background(), querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Failed),
	}).Return(nil)

	err = handler.HandleFailed(context.Background(), nil, task, err)
	require.NoError(t, err)
}
