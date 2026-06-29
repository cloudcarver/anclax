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

type fakeTaskLifeCycleHandler struct {
	handleAttributes func(ctx context.Context, tx core.Tx, task apigen.Task) error
	handleFailed     func(ctx context.Context, tx core.Tx, task apigen.Task, execErr error) error
	handleCompleted  func(ctx context.Context, tx core.Tx, task apigen.Task) error
}

func (h *fakeTaskLifeCycleHandler) HandleAttributes(ctx context.Context, tx core.Tx, task apigen.Task) error {
	if h.handleAttributes != nil {
		return h.handleAttributes(ctx, tx, task)
	}
	return nil
}

func (h *fakeTaskLifeCycleHandler) HandleFailed(ctx context.Context, tx core.Tx, task apigen.Task, execErr error) error {
	if h.handleFailed != nil {
		return h.handleFailed(ctx, tx, task, execErr)
	}
	return nil
}

func (h *fakeTaskLifeCycleHandler) HandleCompleted(ctx context.Context, tx core.Tx, task apigen.Task) error {
	if h.handleCompleted != nil {
		return h.handleCompleted(ctx, tx, task)
	}
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

func TestHasUserClaimLabels(t *testing.T) {
	t.Run("only reserved worker labels", func(t *testing.T) {
		require.False(t, hasUserClaimLabels([]string{"worker:abc", "worker:def"}))
	})
	t.Run("contains non-reserved label", func(t *testing.T) {
		require.True(t, hasUserClaimLabels([]string{"worker:abc", "ops"}))
	})
	t.Run("empty labels", func(t *testing.T) {
		require.False(t, hasUserClaimLabels(nil))
	})
}

func TestNewModelPortHasLabelsIgnoresReservedWorkerLabel(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)

	port, err := NewModelPort(mockModel, workerID, []string{"worker:" + workerID.String()}, nil, 5*time.Second, 0)
	require.NoError(t, err)
	require.False(t, port.hasLabels)

	port2, err := NewModelPort(mockModel, workerID, []string{"worker:" + workerID.String(), "ops"}, nil, 5*time.Second, 0)
	require.NoError(t, err)
	require.True(t, port2.hasLabels)
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
	require.ErrorIs(t, port.taskInterruptCauseFromStore(context.Background(), taskID), taskcore.ErrTaskInterrupted)
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

func TestClaimPathsHasLabelsMatrix(t *testing.T) {
	t.Run("strict claim uses hasLabels=false for internal-only labels", func(t *testing.T) {
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
		mockTxModel.EXPECT().ClaimStrictTask(context.Background(), gomock.AssignableToTypeOf(querier.ClaimStrictTaskParams{})).DoAndReturn(
			func(ctx context.Context, params querier.ClaimStrictTaskParams) (*querier.AnclaxTask, error) {
				require.Equal(t, labels, params.Labels)
				require.False(t, params.HasLabels)
				return nil, pgx.ErrNoRows
			},
		)

		_, err = port.ClaimStrict(context.Background(), ClaimRequest{})
		require.ErrorIs(t, err, ErrNoTask)
	})

	t.Run("normal claim uses hasLabels=true for business labels", func(t *testing.T) {
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
		mockTxModel.EXPECT().ClaimNormalTaskByGroup(context.Background(), gomock.AssignableToTypeOf(querier.ClaimNormalTaskByGroupParams{})).DoAndReturn(
			func(ctx context.Context, params querier.ClaimNormalTaskByGroupParams) (*querier.AnclaxTask, error) {
				require.Equal(t, labels, params.Labels)
				require.True(t, params.HasLabels)
				return nil, pgx.ErrNoRows
			},
		)

		_, err = port.ClaimNormalByGroup(context.Background(), ClaimNormalRequest{Group: DefaultWeightGroup})
		require.ErrorIs(t, err, ErrNoTask)
	})

	t.Run("claim-by-id uses hasLabels=false for internal-only labels", func(t *testing.T) {
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
		mockTxModel.EXPECT().ClaimTaskByID(context.Background(), gomock.AssignableToTypeOf(querier.ClaimTaskByIDParams{})).DoAndReturn(
			func(ctx context.Context, params querier.ClaimTaskByIDParams) (*querier.AnclaxTask, error) {
				require.Equal(t, labels, params.Labels)
				require.False(t, params.HasLabels)
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
	port.registerTaskRuntime(taskID, cancel)
	defer port.completeTaskRuntime(taskID)

	mockModel.EXPECT().RefreshTaskLock(gomock.Any(), gomock.AssignableToTypeOf(querier.RefreshTaskLockParams{})).Return(int32(0), stdErrors.New("transient db error")).MinTimes(1)

	stopRefresh := port.startLockRefresh(ctx, taskID)
	defer stopRefresh()
	time.Sleep(8 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatalf("transient refresh error should not interrupt task: %v", context.Cause(ctx))
	default:
	}
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
	port.registerTaskRuntime(taskID, cancel)
	defer port.completeTaskRuntime(taskID)

	gomock.InOrder(
		mockModel.EXPECT().RefreshTaskLock(gomock.Any(), gomock.AssignableToTypeOf(querier.RefreshTaskLockParams{})).Return(int32(0), stdErrors.New("transient db error")),
		mockModel.EXPECT().RefreshTaskLock(gomock.Any(), gomock.AssignableToTypeOf(querier.RefreshTaskLockParams{})).Return(int32(0), pgx.ErrNoRows),
		mockModel.EXPECT().GetTaskByID(gomock.Any(), taskID).Return(&querier.AnclaxTask{Status: string(apigen.Cancelled)}, nil),
	)

	stopRefresh := port.startLockRefresh(ctx, taskID)
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
	port.registerTaskRuntime(taskID, cancel)

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- port.WaitTaskRuntimes(context.Background(), []int32{taskID})
	}()

	select {
	case err := <-waitDone:
		t.Fatalf("wait returned before runtime completed: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	port.completeTaskRuntime(taskID)
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
	port.registerTaskRuntime(taskID, cancelRuntime)
	defer port.completeTaskRuntime(taskID)

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
	port.registerTaskRuntime(taskID, cancel)
	defer port.completeTaskRuntime(taskID)

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
	port.registerTaskRuntime(taskID, cancel)

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
