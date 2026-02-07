package taskcore

import (
	"context"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestWaitForTaskCompleted(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(1)

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(&querier.AnclaxTask{
		ID:     taskID,
		Status: string(apigen.Completed),
	}, nil)

	store := &TaskStore{model: mockModel}
	err := store.WaitForTask(ctx, taskID)
	require.NoError(t, err)
}

func TestWaitForTaskFailed(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	createdAt := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	taskID := int32(2)

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(&querier.AnclaxTask{
		ID:       taskID,
		Status:   string(apigen.Failed),
		Attempts: 3,
		Attributes: apigen.TaskAttributes{
			RetryPolicy: &apigen.TaskRetryPolicy{Interval: "1s", MaxAttempts: 3},
		},
	}, nil)
	mockModel.EXPECT().GetLastTaskErrorEvent(ctx, taskID).Return(&querier.AnclaxEvent{
		ID:        11,
		Spec:      apigen.EventSpec{Type: apigen.TaskError, TaskError: &apigen.EventTaskError{TaskID: taskID, Error: "boom"}},
		CreatedAt: createdAt,
	}, nil)

	store := &TaskStore{model: mockModel}
	err := store.WaitForTask(ctx, taskID)
	require.Error(t, err)
	require.ErrorContains(t, err, "task 2 failed")
	require.ErrorContains(t, err, "attempts=3")
	require.ErrorContains(t, err, "max_attempts=3")
	require.ErrorContains(t, err, "last_error=boom")
}

func TestWaitForTaskContextCanceled(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	taskID := int32(3)

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(&querier.AnclaxTask{
		ID:     taskID,
		Status: string(apigen.Pending),
	}, nil)

	store := &TaskStore{model: mockModel}
	err := store.WaitForTask(ctx, taskID)
	require.Error(t, err)
	require.ErrorContains(t, err, "wait for task")
	require.ErrorContains(t, err, "context canceled")
}
