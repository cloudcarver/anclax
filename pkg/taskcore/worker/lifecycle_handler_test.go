package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type fakeTx struct{}

func (t *fakeTx) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (t *fakeTx) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, nil
}

func (t *fakeTx) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	return pgx.Row(nil)
}

func (t *fakeTx) Commit(context.Context) error {
	return nil
}

func (t *fakeTx) Rollback(context.Context) error {
	return nil
}

func newLifecycleHandler(model model.ModelInterface, handler TaskHandler, workerID uuid.UUID, now time.Time) *TaskLifeCycleHandler {
	return &TaskLifeCycleHandler{
		model:       model,
		taskHandler: handler,
		workerID:    workerID,
		now:         func() time.Time { return now },
	}
}

func TestHandleFailedStatusOverride(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	workerID := uuid.New()
	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)

	mockModel.EXPECT().UpdateTaskStatusByWorker(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, params querier.UpdateTaskStatusByWorkerParams) (int32, error) {
			require.Equal(t, int32(7), params.ID)
			require.Equal(t, string(apigen.Paused), params.Status)
			require.Equal(t, uuid.NullUUID{UUID: workerID, Valid: true}, params.WorkerID)
			return params.ID, nil
		},
	)

	h := newLifecycleHandler(mockModel, nil, workerID, time.Now())
	err := h.HandleFailed(ctx, &fakeTx{}, apigen.Task{ID: 7}, taskcore.ErrTaskPaused)
	require.NoError(t, err)
}

func TestHandleFailedLockLostShortCircuit(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	h := newLifecycleHandler(model.NewMockModelInterfaceWithTransaction(ctrl), nil, uuid.New(), time.Now())
	err := h.HandleFailed(context.Background(), &fakeTx{}, apigen.Task{ID: 5}, taskcore.ErrTaskLockLost)
	require.ErrorIs(t, err, taskcore.ErrTaskLockLost)
}

func TestHandleFailedRetriesAndReleasesLock(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	now := time.Date(2025, 4, 2, 9, 0, 0, 0, time.UTC)
	workerID := uuid.New()
	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)

	mockModel.EXPECT().UpdateTaskStartedAtByWorker(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, params querier.UpdateTaskStartedAtByWorkerParams) (int32, error) {
			require.Equal(t, int32(9), params.ID)
			require.Equal(t, now.Add(10*time.Second), *params.StartedAt)
			require.Equal(t, uuid.NullUUID{UUID: workerID, Valid: true}, params.WorkerID)
			return params.ID, nil
		},
	)
	mockModel.EXPECT().InsertEvent(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, spec apigen.EventSpec) (*querier.AnclaxEvent, error) {
			require.Equal(t, apigen.TaskError, spec.Type)
			require.NotNil(t, spec.TaskError)
			require.Equal(t, int32(9), spec.TaskError.TaskID)
			require.Equal(t, "boom", spec.TaskError.Error)
			return &querier.AnclaxEvent{ID: 1}, nil
		},
	)
	mockModel.EXPECT().ReleaseTaskLockByWorker(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, params querier.ReleaseTaskLockByWorkerParams) (int32, error) {
			require.Equal(t, int32(9), params.ID)
			require.Equal(t, uuid.NullUUID{UUID: workerID, Valid: true}, params.WorkerID)
			return params.ID, nil
		},
	)

	h := newLifecycleHandler(mockModel, nil, workerID, now)
	task := apigen.Task{
		ID:       9,
		Attempts: 1,
		Attributes: apigen.TaskAttributes{
			RetryPolicy: &apigen.TaskRetryPolicy{Interval: "10s", MaxAttempts: 3},
		},
	}
	err := h.HandleFailed(ctx, &fakeTx{}, task, errors.New("boom"))
	require.NoError(t, err)
}

func TestHandleFailedRetrySkipsErrorEvent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	now := time.Date(2025, 4, 2, 10, 0, 0, 0, time.UTC)
	workerID := uuid.New()
	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)

	mockModel.EXPECT().UpdateTaskStartedAtByWorker(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, params querier.UpdateTaskStartedAtByWorkerParams) (int32, error) {
			require.Equal(t, now.Add(5*time.Second), *params.StartedAt)
			return params.ID, nil
		},
	)
	mockModel.EXPECT().ReleaseTaskLockByWorker(ctx, gomock.Any()).Return(int32(3), nil)
	mockModel.EXPECT().InsertEvent(ctx, gomock.Any()).Times(0)

	h := newLifecycleHandler(mockModel, nil, workerID, now)
	task := apigen.Task{
		ID:       3,
		Attempts: 0,
		Attributes: apigen.TaskAttributes{
			RetryPolicy: &apigen.TaskRetryPolicy{Interval: "5s", MaxAttempts: 2},
		},
	}
	err := h.HandleFailed(ctx, &fakeTx{}, task, taskcore.ErrRetryTaskWithoutErrorEvent)
	require.NoError(t, err)
}

func TestHandleFailedPermanentFailureCallsHook(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	workerID := uuid.New()
	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	mockModel.EXPECT().InsertEvent(ctx, gomock.Any()).Return(&querier.AnclaxEvent{ID: 1}, nil)
	mockModel.EXPECT().UpdateTaskStatusByWorker(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, params querier.UpdateTaskStatusByWorkerParams) (int32, error) {
			require.Equal(t, string(apigen.Failed), params.Status)
			return params.ID, nil
		},
	)
	mockHandler.EXPECT().OnTaskFailed(ctx, gomock.Any(), gomock.Any(), int32(11)).Return(nil)

	h := newLifecycleHandler(mockModel, mockHandler, workerID, time.Now())
	task := apigen.Task{ID: 11, Attributes: apigen.TaskAttributes{RetryPolicy: &apigen.TaskRetryPolicy{Interval: "1s", MaxAttempts: 1}}, Attempts: 1, Spec: apigen.TaskSpec{Type: "demo"}}
	err := h.HandleFailed(ctx, &fakeTx{}, task, errors.New("boom"))
	require.NoError(t, err)
}

func TestHandleFailedHookIgnoresUnknownTask(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	workerID := uuid.New()
	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	mockModel.EXPECT().InsertEvent(ctx, gomock.Any()).Return(&querier.AnclaxEvent{ID: 1}, nil)
	mockModel.EXPECT().UpdateTaskStatusByWorker(ctx, gomock.Any()).Return(int32(12), nil)
	mockHandler.EXPECT().OnTaskFailed(ctx, gomock.Any(), gomock.Any(), int32(12)).Return(ErrUnknownTaskType)

	h := newLifecycleHandler(mockModel, mockHandler, workerID, time.Now())
	task := apigen.Task{ID: 12, Spec: apigen.TaskSpec{Type: "demo"}}
	err := h.HandleFailed(ctx, &fakeTx{}, task, errors.New("boom"))
	require.NoError(t, err)
}

func TestHandleFailedInvalidRetryInterval(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	h := newLifecycleHandler(model.NewMockModelInterfaceWithTransaction(ctrl), nil, uuid.New(), time.Now())
	task := apigen.Task{
		ID:       20,
		Attempts: 0,
		Attributes: apigen.TaskAttributes{
			RetryPolicy: &apigen.TaskRetryPolicy{Interval: "bad", MaxAttempts: 3},
		},
	}
	err := h.HandleFailed(context.Background(), &fakeTx{}, task, errors.New("boom"))
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid retry policy interval")
}

func TestHandleCompletedCronjobReschedules(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	now := time.Date(2025, 4, 2, 12, 0, 0, 0, time.UTC)
	workerID := uuid.New()
	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)

	nextTime, err := nextCronTime("*/5 * * * * *", now)
	require.NoError(t, err)

	mockModel.EXPECT().UpdateTaskStartedAtByWorker(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, params querier.UpdateTaskStartedAtByWorkerParams) (int32, error) {
			require.Equal(t, nextTime, *params.StartedAt)
			return params.ID, nil
		},
	)
	mockModel.EXPECT().ReleaseTaskLockByWorker(ctx, gomock.Any()).Return(int32(8), nil)

	h := newLifecycleHandler(mockModel, nil, workerID, now)
	task := apigen.Task{ID: 8, Attributes: apigen.TaskAttributes{Cronjob: &apigen.TaskCronjob{CronExpression: "*/5 * * * * *"}}}
	err = h.HandleCompleted(ctx, &fakeTx{}, task)
	require.NoError(t, err)
}

func TestHandleCompletedUpdatesStatus(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	workerID := uuid.New()
	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)

	mockModel.EXPECT().UpdateTaskStatusByWorker(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, params querier.UpdateTaskStatusByWorkerParams) (int32, error) {
			require.Equal(t, string(apigen.Completed), params.Status)
			return params.ID, nil
		},
	)
	mockModel.EXPECT().InsertEvent(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, spec apigen.EventSpec) (*querier.AnclaxEvent, error) {
			require.Equal(t, apigen.TaskCompleted, spec.Type)
			require.NotNil(t, spec.TaskCompleted)
			require.Equal(t, int32(6), spec.TaskCompleted.TaskID)
			return &querier.AnclaxEvent{ID: 2}, nil
		},
	)

	h := newLifecycleHandler(mockModel, nil, workerID, time.Now())
	task := apigen.Task{ID: 6}
	err := h.HandleCompleted(ctx, &fakeTx{}, task)
	require.NoError(t, err)
}

func TestHandleCompletedInvalidCronExpression(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	h := newLifecycleHandler(model.NewMockModelInterfaceWithTransaction(ctrl), nil, uuid.New(), time.Now())
	task := apigen.Task{ID: 4, Attributes: apigen.TaskAttributes{Cronjob: &apigen.TaskCronjob{CronExpression: "bad"}}}
	err := h.HandleCompleted(context.Background(), &fakeTx{}, task)
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid cron expression")
}
