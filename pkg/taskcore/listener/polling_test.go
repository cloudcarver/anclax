package listener

import (
	"context"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestWaitTaskReturnsTerminalStatusImmediately(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(1)
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskWaitStatusByID(ctx, taskID).Return(&querier.GetTaskWaitStatusByIDRow{
		ID:     taskID,
		Status: string(apigen.Completed),
	}, nil)

	l := NewPollingTaskEventListener(mockModel)
	defer func() {
		require.NoError(t, l.Close(context.Background()))
	}()

	ch, err := l.WaitTask(ctx, taskID)
	require.NoError(t, err)

	event, ok := <-ch
	require.True(t, ok)
	require.Equal(t, taskID, event.TaskID)
	require.Equal(t, apigen.Completed, event.Status)
	require.NoError(t, event.Err)

	_, ok = <-ch
	require.False(t, ok)
}

func TestWaitTaskReturnsNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(404)
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskWaitStatusByID(ctx, taskID).Return(nil, pgx.ErrNoRows)

	l := NewPollingTaskEventListener(mockModel)
	defer func() {
		require.NoError(t, l.Close(context.Background()))
	}()

	_, err := l.WaitTask(ctx, taskID)
	require.ErrorIs(t, err, ErrTaskNotFound)
}

func TestWaitTaskPollsPendingTaskUntilTerminal(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(2)
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskWaitStatusByID(ctx, taskID).Return(&querier.GetTaskWaitStatusByIDRow{
		ID:     taskID,
		Status: string(apigen.Pending),
	}, nil)
	mockModel.EXPECT().ListTerminalTaskWaitStatuses(gomock.Any(), []int32{taskID}).Return([]*querier.ListTerminalTaskWaitStatusesRow{
		{ID: taskID, Status: string(apigen.Failed)},
	}, nil)

	l := NewPollingTaskEventListener(mockModel)
	defer func() {
		require.NoError(t, l.Close(context.Background()))
	}()

	ch, err := l.WaitTask(ctx, taskID)
	require.NoError(t, err)
	l.poll(ctx)

	event := readListenerEvent(t, ch)
	require.Equal(t, taskID, event.TaskID)
	require.Equal(t, apigen.Failed, event.Status)
	require.NoError(t, event.Err)
}

func TestWaitTaskNotifiesMultipleSubscribers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(3)
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskWaitStatusByID(ctx, taskID).Return(&querier.GetTaskWaitStatusByIDRow{
		ID:     taskID,
		Status: string(apigen.Pending),
	}, nil).Times(2)
	mockModel.EXPECT().ListTerminalTaskWaitStatuses(gomock.Any(), []int32{taskID}).Return([]*querier.ListTerminalTaskWaitStatusesRow{
		{ID: taskID, Status: string(apigen.Completed)},
	}, nil)

	l := NewPollingTaskEventListener(mockModel)
	defer func() {
		require.NoError(t, l.Close(context.Background()))
	}()

	ch1, err := l.WaitTask(ctx, taskID)
	require.NoError(t, err)
	ch2, err := l.WaitTask(ctx, taskID)
	require.NoError(t, err)
	l.poll(ctx)

	require.Equal(t, apigen.Completed, readListenerEvent(t, ch1).Status)
	require.Equal(t, apigen.Completed, readListenerEvent(t, ch2).Status)
}

func TestPollingSkipsQueryWhenNoWatchers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	l := NewPollingTaskEventListener(mockModel)
	l.poll(context.Background())
	require.NoError(t, l.Close(context.Background()))
}

func readListenerEvent(t *testing.T, ch <-chan TaskTerminalEvent) TaskTerminalEvent {
	t.Helper()
	select {
	case event, ok := <-ch:
		require.True(t, ok)
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for listener event")
		return TaskTerminalEvent{}
	}
}
