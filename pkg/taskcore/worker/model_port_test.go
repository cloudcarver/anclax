package worker

import (
	"context"
	stdErrors "errors"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type fakeTaskLifeCycleHandler struct{}

func (*fakeTaskLifeCycleHandler) FinalizeAttempt(context.Context, core.Tx, Task, error) error {
	return nil
}

func TestModelPortClaimByIDNoTask(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	mockTxModel := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTx := core.NewMockTx(ctrl)

	port, err := NewModelPort(mockModel, workerID, []string{"ops"}, nil, 5*time.Second, 0)
	require.NoError(t, err)

	mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			return f(mockTx, mockTxModel)
		},
	).Times(1)
	mockModel.EXPECT().MaintainTaskConcurrency(gomock.Any(), int64(5000)).Return(nil).Times(1)
	mockTxModel.EXPECT().ClaimTaskByID(context.Background(), gomock.AssignableToTypeOf(querier.ClaimTaskByIDParams{})).
		Return(nil, pgx.ErrNoRows).Times(1)

	task, err := port.ClaimByID(context.Background(), 42, ClaimRequest{})
	require.Nil(t, task)
	require.ErrorIs(t, err, ErrNoTask)
}

func TestModelPortRefreshRuntimeConfigDecode(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, nil, nil, 5*time.Second, 0)
	require.NoError(t, err)

	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(context.Background()).Return(&querier.AnclaxWorkerRuntimeConfig{
		Version: 7,
		Payload: []byte(`{"maxStrictPercentage":30,"labelWeights":{"default":1,"w1":3}}`),
	}, nil)

	cfg, err := port.RefreshRuntimeConfig(context.Background(), workerID.String(), "")
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, int64(7), cfg.Version)
	require.NotNil(t, cfg.MaxStrictPercentage)
	require.Equal(t, int32(30), *cfg.MaxStrictPercentage)
	require.Equal(t, int32(3), cfg.LabelWeights["w1"])
}

func TestModelPortTaskInterruptCauseFromStore(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, nil, nil, 5*time.Second, 0)
	require.NoError(t, err)

	taskID := int32(9)

	mockModel.EXPECT().GetTaskByID(context.Background(), taskID).Return(&querier.AnclaxTask{Status: string(apigen.Paused)}, nil)
	require.ErrorIs(t, port.taskInterruptCauseFromStore(context.Background(), taskID), taskcore.ErrTaskPaused)

	mockModel.EXPECT().GetTaskByID(context.Background(), taskID).Return(&querier.AnclaxTask{Status: string(apigen.Cancelled)}, nil)
	require.ErrorIs(t, port.taskInterruptCauseFromStore(context.Background(), taskID), taskcore.ErrTaskCancelled)

	mockModel.EXPECT().GetTaskByID(context.Background(), taskID).Return(nil, pgx.ErrNoRows)
	require.ErrorIs(t, port.taskInterruptCauseFromStore(context.Background(), taskID), taskcore.ErrTaskLockLost)
}

func TestModelPortAckRuntimeConfigApplied(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, nil, nil, 5*time.Second, 0)
	require.NoError(t, err)

	const requestID = "req-77"
	const appliedVersion int64 = 11
	mockModel.EXPECT().UpdateWorkerAppliedConfigVersion(context.Background(), querier.UpdateWorkerAppliedConfigVersionParams{
		ID:                   workerID,
		AppliedConfigVersion: appliedVersion,
	}).Return(nil)

	err = port.AckRuntimeConfigApplied(context.Background(), workerID.String(), requestID, appliedVersion)
	require.NoError(t, err)
}

func TestClaimPathsLabelsAndTTLMatrix(t *testing.T) {
	t.Run("strict claim passes internal labels", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		workerID := uuid.New()
		mockModel := model.NewMockModelInterface(ctrl)
		mockTxModel := model.NewMockModelInterfaceWithTransaction(ctrl)
		mockTx := core.NewMockTx(ctrl)

		labels := []string{"worker:" + workerID.String()}
		port, err := NewModelPort(mockModel, workerID, labels, nil, 5*time.Second, 0)
		require.NoError(t, err)

		mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
				return f(mockTx, mockTxModel)
			},
		)
		mockModel.EXPECT().MaintainTaskConcurrency(gomock.Any(), int64(5000)).Return(nil).Times(1)
		mockTxModel.EXPECT().ClaimStrictTask(context.Background(), gomock.AssignableToTypeOf(querier.ClaimStrictTaskParams{})).DoAndReturn(
			func(ctx context.Context, params querier.ClaimStrictTaskParams) (*querier.AnclaxTask, error) {
				require.Equal(t, labels, params.Labels)
				require.Equal(t, int64(5000), params.LockTtlMs)
				return nil, pgx.ErrNoRows
			},
		)

		_, err = port.ClaimStrict(context.Background(), ClaimRequest{})
		require.ErrorIs(t, err, ErrNoTask)
	})

	t.Run("normal claim passes all business labels", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		workerID := uuid.New()
		mockModel := model.NewMockModelInterface(ctrl)
		mockTxModel := model.NewMockModelInterfaceWithTransaction(ctrl)
		mockTx := core.NewMockTx(ctrl)

		labels := []string{"worker:" + workerID.String(), "gpu", "arm"}
		port, err := NewModelPort(mockModel, workerID, labels, nil, 5*time.Second, 0)
		require.NoError(t, err)

		mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
				return f(mockTx, mockTxModel)
			},
		)
		mockModel.EXPECT().MaintainTaskConcurrency(gomock.Any(), int64(5000)).Return(nil).Times(1)
		mockTxModel.EXPECT().ClaimNormalTaskByGroup(context.Background(), gomock.AssignableToTypeOf(querier.ClaimNormalTaskByGroupParams{})).DoAndReturn(
			func(ctx context.Context, params querier.ClaimNormalTaskByGroupParams) (*querier.AnclaxTask, error) {
				require.Equal(t, labels, params.Labels)
				require.Equal(t, int64(5000), params.LockTtlMs)
				return nil, pgx.ErrNoRows
			},
		)

		_, err = port.ClaimNormalByGroup(context.Background(), ClaimNormalRequest{Group: DefaultWeightGroup})
		require.ErrorIs(t, err, ErrNoTask)
	})

	t.Run("claim-by-id passes internal labels", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		workerID := uuid.New()
		mockModel := model.NewMockModelInterface(ctrl)
		mockTxModel := model.NewMockModelInterfaceWithTransaction(ctrl)
		mockTx := core.NewMockTx(ctrl)

		labels := []string{"worker:" + workerID.String()}
		port, err := NewModelPort(mockModel, workerID, labels, nil, 5*time.Second, 0)
		require.NoError(t, err)

		mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
				return f(mockTx, mockTxModel)
			},
		)
		mockModel.EXPECT().MaintainTaskConcurrency(gomock.Any(), int64(5000)).Return(nil).Times(1)
		mockTxModel.EXPECT().ClaimTaskByID(context.Background(), gomock.AssignableToTypeOf(querier.ClaimTaskByIDParams{})).DoAndReturn(
			func(ctx context.Context, params querier.ClaimTaskByIDParams) (*querier.AnclaxTask, error) {
				require.Equal(t, labels, params.Labels)
				require.Equal(t, int64(5000), params.LockTtlMs)
				return nil, pgx.ErrNoRows
			},
		)

		_, err = port.ClaimByID(context.Background(), 123, ClaimRequest{})
		require.ErrorIs(t, err, ErrNoTask)
	})
}

func TestStartLockRefreshTransientErrorsDoNotInterrupt(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, []string{"ops"}, nil, 5*time.Second, 2*time.Millisecond)
	require.NoError(t, err)

	taskID := int32(77)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	port.registerTaskRuntime(Task{ID: taskID}, cancel)
	defer port.completeTaskRuntime(Task{ID: taskID})

	mockModel.EXPECT().RefreshTaskLock(gomock.Any(), gomock.AssignableToTypeOf(querier.RefreshTaskLockParams{})).Return(int32(0), stdErrors.New("transient db error")).MinTimes(1)

	stopRefresh := port.startLockRefresh(ctx, Task{ID: taskID})
	defer stopRefresh()
	time.Sleep(8 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatalf("transient refresh error should not interrupt task: %v", context.Cause(ctx))
	default:
	}
}

func TestLockRefreshStopsAtLeaseDeadlineAfterTransientErrors(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := model.NewMockModelInterface(ctrl)
	p, err := NewModelPort(m, uuid.New(), nil, nil, 70*time.Millisecond, 30*time.Millisecond)
	require.NoError(t, err)
	task := Task{ID: 99, LeaseVersion: 4, claimedAt: time.Now()}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	p.registerTaskRuntime(task, cancel)
	defer p.completeTaskRuntime(task)
	m.EXPECT().RefreshTaskLock(gomock.Any(), gomock.Any()).Return(int32(0), stdErrors.New("database unavailable")).MinTimes(1)
	stop := p.startLockRefresh(ctx, task)
	defer stop()
	select {
	case <-ctx.Done():
		require.ErrorIs(t, context.Cause(ctx), taskcore.ErrTaskLockLost)
	case <-time.After(time.Second):
		t.Fatal("lease expiry did not cancel the executor")
	}
}

func TestOldAttemptCannotInterruptOrCompleteNewAttempt(t *testing.T) {
	p, err := NewModelPort(nil, uuid.New(), nil, nil, time.Second, 0)
	require.NoError(t, err)
	old, current := Task{ID: 7, LeaseVersion: 1}, Task{ID: 7, LeaseVersion: 2}
	oldCtx, oldCancel := context.WithCancelCause(context.Background())
	newCtx, newCancel := context.WithCancelCause(context.Background())
	defer oldCancel(nil)
	defer newCancel(nil)
	p.registerTaskRuntime(old, oldCancel)
	p.registerTaskRuntime(current, newCancel)
	defer p.completeTaskRuntime(current)
	p.interruptAttempt(old, taskcore.ErrTaskLockLost)
	p.completeTaskRuntime(old)
	require.ErrorIs(t, context.Cause(oldCtx), taskcore.ErrTaskLockLost)
	require.NoError(t, newCtx.Err())
	require.True(t, p.TaskRuntimesActive([]int32{7}))
	p.InterruptTask(7, taskcore.ErrTaskCancelled)
	require.ErrorIs(t, context.Cause(newCtx), taskcore.ErrTaskCancelled)
}

func TestStartLockRefreshInterruptsOnLockLossAfterTransientError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, []string{"ops"}, nil, 5*time.Second, 2*time.Millisecond)
	require.NoError(t, err)

	taskID := int32(88)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	port.registerTaskRuntime(Task{ID: taskID}, cancel)
	defer port.completeTaskRuntime(Task{ID: taskID})

	gomock.InOrder(
		mockModel.EXPECT().RefreshTaskLock(gomock.Any(), gomock.AssignableToTypeOf(querier.RefreshTaskLockParams{})).Return(int32(0), stdErrors.New("transient db error")),
		mockModel.EXPECT().RefreshTaskLock(gomock.Any(), gomock.AssignableToTypeOf(querier.RefreshTaskLockParams{})).Return(int32(0), pgx.ErrNoRows),
		mockModel.EXPECT().GetTaskByID(gomock.Any(), taskID).Return(&querier.AnclaxTask{Status: string(apigen.Cancelled)}, nil),
	)

	stopRefresh := port.startLockRefresh(ctx, Task{ID: taskID})
	defer stopRefresh()
	require.Eventually(t, func() bool {
		return context.Cause(ctx) != nil
	}, time.Second, 5*time.Millisecond)
	require.ErrorIs(t, context.Cause(ctx), taskcore.ErrTaskCancelled)
}

func TestWaitTaskRuntimesWaitsForFinalizeCompletion(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, []string{"ops"}, nil, 5*time.Second, 0)
	require.NoError(t, err)

	taskID := int32(99)
	_, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	port.registerTaskRuntime(Task{ID: taskID}, cancel)

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- port.WaitTaskRuntimes(context.Background(), []int32{taskID})
	}()

	select {
	case err := <-waitDone:
		t.Fatalf("wait returned before runtime completed: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	port.completeTaskRuntime(Task{ID: taskID})
	require.NoError(t, <-waitDone)
}

func TestWaitTaskRuntimesReturnsContextError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, []string{"ops"}, nil, 5*time.Second, 0)
	require.NoError(t, err)

	taskID := int32(99)
	_, cancelRuntime := context.WithCancelCause(context.Background())
	defer cancelRuntime(nil)
	port.registerTaskRuntime(Task{ID: taskID}, cancelRuntime)
	defer port.completeTaskRuntime(Task{ID: taskID})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, port.WaitTaskRuntimes(ctx, []int32{taskID}), context.Canceled)
}

func TestWaitTaskRuntimesMissingEntryReturnsImmediately(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, []string{"ops"}, nil, 5*time.Second, 0)
	require.NoError(t, err)

	require.NoError(t, port.WaitTaskRuntimes(context.Background(), []int32{123}))
}

func TestInterruptTaskCancelsRuntimeWithCause(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, []string{"ops"}, nil, 5*time.Second, 0)
	require.NoError(t, err)

	taskID := int32(123)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	port.registerTaskRuntime(Task{ID: taskID}, cancel)
	defer port.completeTaskRuntime(Task{ID: taskID})

	port.InterruptTask(taskID, taskcore.ErrTaskPaused)
	require.ErrorIs(t, context.Cause(ctx), taskcore.ErrTaskPaused)
}

func TestFinalizeTaskCompletesRuntimeEntry(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, []string{"ops"}, nil, 5*time.Second, 0)
	require.NoError(t, err)
	port.lifeCycleHandler = &fakeTaskLifeCycleHandler{}

	taskID := int32(456)
	_, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	port.registerTaskRuntime(Task{ID: taskID}, cancel)

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- port.WaitTaskRuntimes(context.Background(), []int32{taskID})
	}()
	select {
	case err := <-waitDone:
		t.Fatalf("wait returned before finalize completed runtime entry: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			return f(&fakeTx{}, mockModel)
		},
	)
	require.NoError(t, port.FinalizeTask(context.Background(), Task{ID: taskID}, nil))
	require.NoError(t, <-waitDone)
}

func TestTaskInterruptCauseRecognizesPauseAndCancel(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, []string{"ops"}, nil, 5*time.Second, 0)
	require.NoError(t, err)

	cancelCtx, cancel := context.WithCancelCause(context.Background())
	cancel(taskcore.ErrTaskCancelled)
	require.ErrorIs(t, port.taskInterruptCause(cancelCtx), taskcore.ErrTaskCancelled)

	pauseCtx, pause := context.WithCancelCause(context.Background())
	pause(taskcore.ErrTaskPaused)
	require.ErrorIs(t, port.taskInterruptCause(pauseCtx), taskcore.ErrTaskPaused)
}
