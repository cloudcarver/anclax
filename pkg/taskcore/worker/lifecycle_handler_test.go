package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/taskcore"
	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
		currTime      = time.Date(2025, 3, 27, 0, 0, 1, 0, location)
		cronExpr      = "0 0 0 * * *"
		nextTime      = time.Date(2025, 3, 28, 0, 0, 0, 0, location)
		taskID        = int32(1)
		workerID      = uuid.New()
		workerIDParam = uuid.NullUUID{UUID: workerID, Valid: true}
	)

	handler := &TaskLifeCycleHandler{
		model:       mockModel,
		taskHandler: mockTaskHandler,
		now: func() time.Time {
			return currTime
		},
		workerID: workerID,
	}

	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)
	mockTxm.EXPECT().UpdateTaskStartedAtByWorker(context.Background(), querier.UpdateTaskStartedAtByWorkerParams{
		ID:        taskID,
		StartedAt: utils.Ptr(nextTime),
		WorkerID:  workerIDParam,
	}).Return(taskID, nil)

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
		taskID        = int32(1)
		workerID      = uuid.New()
		workerIDParam = uuid.NullUUID{UUID: workerID, Valid: true}
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model:       mockModel,
		taskHandler: mockTaskHandler,
		workerID:    workerID,
	}

	task := apigen.Task{
		ID: taskID,
	}

	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)
	mockTxm.EXPECT().VerifyTaskOwnership(context.Background(), querier.VerifyTaskOwnershipParams{
		ID:       taskID,
		WorkerID: workerIDParam,
	}).Return(taskID, nil)
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskCompleted,
		TaskCompleted: &apigen.EventTaskCompleted{
			TaskID: taskID,
		},
	}).Return(&querier.AnclaxEvent{}, nil)

	mockTxm.EXPECT().UpdateTaskStatusByWorker(context.Background(), querier.UpdateTaskStatusByWorkerParams{
		ID:       taskID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}).Return(taskID, nil)

	err := handler.HandleCompleted(context.Background(), nil, task)
	require.NoError(t, err)
}

func TestHandleFailed(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID        = int32(1)
		err           = errors.New("test error")
		workerID      = uuid.New()
		workerIDParam = uuid.NullUUID{UUID: workerID, Valid: true}
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model:       mockModel,
		taskHandler: mockTaskHandler,
		workerID:    workerID,
	}

	task := apigen.Task{
		ID: taskID,
		Spec: apigen.TaskSpec{
			Type: "testTask",
		},
	}

	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)
	mockTxm.EXPECT().VerifyTaskOwnership(context.Background(), querier.VerifyTaskOwnershipParams{
		ID:       taskID,
		WorkerID: workerIDParam,
	}).Return(taskID, nil)
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  err.Error(),
		},
	}).Return(&querier.AnclaxEvent{}, nil)

	mockTaskHandler.EXPECT().OnTaskFailed(context.Background(), gomock.Any(), &task.Spec, taskID).Return(nil)

	mockTxm.EXPECT().UpdateTaskStatusByWorker(context.Background(), querier.UpdateTaskStatusByWorkerParams{
		ID:       taskID,
		Status:   string(apigen.Failed),
		WorkerID: workerIDParam,
	}).Return(taskID, nil)

	err = handler.HandleFailed(context.Background(), nil, task, err)
	require.NoError(t, err)
}

func TestHandleFailedWithRetryPolicy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID        = int32(1)
		err           = errors.New("test error")
		intervalRaw   = "1h"
		interval, _   = time.ParseDuration(intervalRaw)
		currTime      = time.Now()
		workerID      = uuid.New()
		workerIDParam = uuid.NullUUID{UUID: workerID, Valid: true}
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model:       mockModel,
		taskHandler: mockTaskHandler,
		now: func() time.Time {
			return currTime
		},
		workerID: workerID,
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
	mockTxm.EXPECT().VerifyTaskOwnership(context.Background(), querier.VerifyTaskOwnershipParams{
		ID:       taskID,
		WorkerID: workerIDParam,
	}).Return(taskID, nil)
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  err.Error(),
		},
	}).Return(&querier.AnclaxEvent{}, nil)

	mockTxm.EXPECT().UpdateTaskStartedAtByWorker(context.Background(), querier.UpdateTaskStartedAtByWorkerParams{
		ID:        taskID,
		StartedAt: utils.Ptr(currTime.Add(interval)),
		WorkerID:  workerIDParam,
	}).Return(taskID, nil)

	mockTxm.EXPECT().ReleaseTaskLockByWorker(context.Background(), querier.ReleaseTaskLockByWorkerParams{
		ID:       taskID,
		WorkerID: workerIDParam,
	}).Return(taskID, nil)

	// Note: OnTaskFailed is not called when task is retried

	err = handler.HandleFailed(context.Background(), nil, task, err)
	require.NoError(t, err)

}

func TestHandleFailed_ErrRetryTaskWithoutErrorEvent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID        = int32(1)
		currTime      = time.Now()
		interval      = 1 * time.Hour
		workerID      = uuid.New()
		workerIDParam = uuid.NullUUID{UUID: workerID, Valid: true}
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model:       mockModel,
		taskHandler: mockTaskHandler,
		now: func() time.Time {
			return currTime
		},
		workerID: workerID,
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
	mockTxm.EXPECT().VerifyTaskOwnership(context.Background(), querier.VerifyTaskOwnershipParams{
		ID:       taskID,
		WorkerID: workerIDParam,
	}).Return(taskID, nil)

	mockTxm.EXPECT().UpdateTaskStartedAtByWorker(context.Background(), querier.UpdateTaskStartedAtByWorkerParams{
		ID:        taskID,
		StartedAt: utils.Ptr(currTime.Add(interval)),
		WorkerID:  workerIDParam,
	}).Return(taskID, nil)

	mockTxm.EXPECT().ReleaseTaskLockByWorker(context.Background(), querier.ReleaseTaskLockByWorkerParams{
		ID:       taskID,
		WorkerID: workerIDParam,
	}).Return(taskID, nil)

	// Note: OnTaskFailed is not called when task is retried

	err := handler.HandleFailed(context.Background(), nil, task, taskcore.ErrRetryTaskWithoutErrorEvent)
	require.NoError(t, err)
}

func TestHandleFailed_ErrFatalTask(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID        = int32(1)
		currTime      = time.Now()
		workerID      = uuid.New()
		workerIDParam = uuid.NullUUID{UUID: workerID, Valid: true}
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model:       mockModel,
		taskHandler: mockTaskHandler,
		now: func() time.Time {
			return currTime
		},
		workerID: workerID,
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
	mockTxm.EXPECT().VerifyTaskOwnership(context.Background(), querier.VerifyTaskOwnershipParams{
		ID:       taskID,
		WorkerID: workerIDParam,
	}).Return(taskID, nil)
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  taskcore.ErrFatalTask.Error(),
		},
	}).Return(&querier.AnclaxEvent{}, nil)

	mockTaskHandler.EXPECT().OnTaskFailed(context.Background(), gomock.Any(), &task.Spec, taskID).Return(nil)

	mockTxm.EXPECT().UpdateTaskStatusByWorker(context.Background(), querier.UpdateTaskStatusByWorkerParams{
		ID:       taskID,
		Status:   string(apigen.Failed),
		WorkerID: workerIDParam,
	}).Return(taskID, nil)

	err := handler.HandleFailed(context.Background(), nil, task, taskcore.ErrFatalTask)
	require.NoError(t, err)
}

func TestHandleFailed_ErrFatalTask_Cronjob(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID        = int32(1)
		workerID      = uuid.New()
		workerIDParam = uuid.NullUUID{UUID: workerID, Valid: true}
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model:       mockModel,
		taskHandler: mockTaskHandler,
		workerID:    workerID,
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
	mockTxm.EXPECT().VerifyTaskOwnership(context.Background(), querier.VerifyTaskOwnershipParams{
		ID:       taskID,
		WorkerID: workerIDParam,
	}).Return(taskID, nil)
	// Expect that error event is still inserted for fatal errors
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  taskcore.ErrFatalTask.Error(),
		},
	}).Return(&querier.AnclaxEvent{}, nil)

	mockTxm.EXPECT().ReleaseTaskLockByWorker(context.Background(), querier.ReleaseTaskLockByWorkerParams{
		ID:       taskID,
		WorkerID: workerIDParam,
	}).Return(taskID, nil)

	// Note: For cronjobs, OnTaskFailed and UpdateTaskStatus should NOT be called
	// because cronjobs are designed to run again regardless of failures

	err := handler.HandleFailed(context.Background(), nil, task, taskcore.ErrFatalTask)
	require.NoError(t, err)
}

func TestHandleFailedWithMaxAttempts(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID        = int32(1)
		err           = errors.New("test error")
		maxAttempts   = int32(3)
		currAttempts  = int32(2)
		intervalRaw   = "1h"
		interval, _   = time.ParseDuration(intervalRaw)
		currTime      = time.Now()
		workerID      = uuid.New()
		workerIDParam = uuid.NullUUID{UUID: workerID, Valid: true}
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model:       mockModel,
		taskHandler: mockTaskHandler,
		now: func() time.Time {
			return currTime
		},
		workerID: workerID,
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
	mockTxm.EXPECT().VerifyTaskOwnership(context.Background(), querier.VerifyTaskOwnershipParams{
		ID:       taskID,
		WorkerID: workerIDParam,
	}).Return(taskID, nil)
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  err.Error(),
		},
	}).Return(&querier.AnclaxEvent{}, nil)

	mockTxm.EXPECT().UpdateTaskStartedAtByWorker(context.Background(), querier.UpdateTaskStartedAtByWorkerParams{
		ID:        taskID,
		StartedAt: utils.Ptr(currTime.Add(interval)),
		WorkerID:  workerIDParam,
	}).Return(taskID, nil)

	mockTxm.EXPECT().ReleaseTaskLockByWorker(context.Background(), querier.ReleaseTaskLockByWorkerParams{
		ID:       taskID,
		WorkerID: workerIDParam,
	}).Return(taskID, nil)

	err = handler.HandleFailed(context.Background(), nil, task, err)
	require.NoError(t, err)
}

func TestHandleFailedExceedsMaxAttempts(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		taskID        = int32(1)
		err           = errors.New("test error")
		maxAttempts   = int32(3)
		currAttempts  = int32(3)
		intervalRaw   = "1h"
		workerID      = uuid.New()
		workerIDParam = uuid.NullUUID{UUID: workerID, Valid: true}
	)

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTaskHandler := NewMockTaskHandler(ctrl)

	handler := &TaskLifeCycleHandler{
		model:       mockModel,
		taskHandler: mockTaskHandler,
		workerID:    workerID,
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
	mockTxm.EXPECT().VerifyTaskOwnership(context.Background(), querier.VerifyTaskOwnershipParams{
		ID:       taskID,
		WorkerID: workerIDParam,
	}).Return(taskID, nil)
	mockTxm.EXPECT().InsertEvent(context.Background(), apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  err.Error(),
		},
	}).Return(&querier.AnclaxEvent{}, nil)

	mockTaskHandler.EXPECT().OnTaskFailed(context.Background(), gomock.Any(), &task.Spec, taskID).Return(nil)

	mockTxm.EXPECT().UpdateTaskStatusByWorker(context.Background(), querier.UpdateTaskStatusByWorkerParams{
		ID:       taskID,
		Status:   string(apigen.Failed),
		WorkerID: workerIDParam,
	}).Return(taskID, nil)

	err = handler.HandleFailed(context.Background(), nil, task, err)
	require.NoError(t, err)
}

func TestHandleAttributesNoCronjobIsNoop(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	handler := &TaskLifeCycleHandler{
		model:       model.NewMockModelInterface(ctrl),
		taskHandler: NewMockTaskHandler(ctrl),
		workerID:    uuid.New(),
	}
	err := handler.HandleAttributes(context.Background(), nil, apigen.Task{
		ID:         1,
		Attributes: apigen.TaskAttributes{},
	})
	require.NoError(t, err)
}

func TestHandleAttributesCronjobInvalidExpression(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockModel.EXPECT().SpawnWithTx(gomock.Any()).Return(mockTxm)

	handler := &TaskLifeCycleHandler{
		model:       mockModel,
		taskHandler: NewMockTaskHandler(ctrl),
		workerID:    uuid.New(),
	}
	err := handler.HandleAttributes(context.Background(), nil, apigen.Task{
		ID: 1,
		Attributes: apigen.TaskAttributes{
			Cronjob: &apigen.TaskCronjob{CronExpression: "bad cron"},
		},
	})
	require.ErrorContains(t, err, "failed to parse cron expression")
}

func TestHandleAttributesCronjobLockLost(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTx := core.NewMockTx(ctrl)
	workerID := uuid.New()
	workerIDParam := uuid.NullUUID{UUID: workerID, Valid: true}

	mockModel.EXPECT().SpawnWithTx(mockTx).Return(mockTxm)
	mockTxm.EXPECT().UpdateTaskStartedAtByWorker(context.Background(), gomock.AssignableToTypeOf(querier.UpdateTaskStartedAtByWorkerParams{})).
		DoAndReturn(func(ctx context.Context, params querier.UpdateTaskStartedAtByWorkerParams) (int32, error) {
			require.Equal(t, int32(7), params.ID)
			require.Equal(t, workerIDParam, params.WorkerID)
			return 0, pgx.ErrNoRows
		})

	handler := &TaskLifeCycleHandler{
		model:       mockModel,
		taskHandler: NewMockTaskHandler(ctrl),
		now: func() time.Time {
			return time.Date(2026, 2, 14, 0, 0, 0, 0, time.UTC)
		},
		workerID: workerID,
	}
	err := handler.HandleAttributes(context.Background(), mockTx, apigen.Task{
		ID: 7,
		Attributes: apigen.TaskAttributes{
			Cronjob: &apigen.TaskCronjob{CronExpression: "0 * * * * *"},
		},
	})
	require.ErrorIs(t, err, taskcore.ErrTaskLockLost)
}
