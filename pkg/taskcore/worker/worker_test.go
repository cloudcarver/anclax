package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/taskcore"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestClaimTaskUsesLabelsAndTTL(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	lockTTL := 9 * time.Second
	labels := []string{"billing"}

	cfg := &config.Config{
		Worker: config.Worker{
			LockTTL: &lockTTL,
			Labels:  labels,
		},
	}

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTx := core.NewMockTx(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	worker, err := NewWorker(globalctx.New(), cfg, mockModel, mockHandler)
	require.NoError(t, err)
	w := worker.(*Worker)

	fixedNow := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	w.now = func() time.Time {
		return fixedNow
	}

	expectedExpiry := fixedNow.Add(-lockTTL)

	mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			return f(mockTx, mockTxm)
		},
	)

	mockTxm.EXPECT().ClaimTask(context.Background(), gomock.AssignableToTypeOf(querier.ClaimTaskParams{})).DoAndReturn(
		func(ctx context.Context, params querier.ClaimTaskParams) (*querier.AnclaxTask, error) {
			require.NotNil(t, params.LockExpiry)
			require.Equal(t, expectedExpiry, *params.LockExpiry)
			require.Equal(t, labels, params.Labels)
			require.True(t, params.HasLabels)
			return &querier.AnclaxTask{
				ID:         1,
				Attributes: apigen.TaskAttributes{},
				Spec:       apigen.TaskSpec{},
				Status:     string(apigen.Pending),
				SerialKey:  nil,
				SerialID:   nil,
			}, nil
		},
	)

	task, err := w.claimTask(context.Background())
	require.NoError(t, err)
	require.NotNil(t, task)
}

func TestClaimTaskWithoutLabels(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	lockTTL := 9 * time.Second

	cfg := &config.Config{
		Worker: config.Worker{
			LockTTL: &lockTTL,
		},
	}

	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTx := core.NewMockTx(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	worker, err := NewWorker(globalctx.New(), cfg, mockModel, mockHandler)
	require.NoError(t, err)
	w := worker.(*Worker)

	fixedNow := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	w.now = func() time.Time {
		return fixedNow
	}

	expectedExpiry := fixedNow.Add(-lockTTL)

	mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			return f(mockTx, mockTxm)
		},
	)

	mockTxm.EXPECT().ClaimTask(context.Background(), gomock.AssignableToTypeOf(querier.ClaimTaskParams{})).DoAndReturn(
		func(ctx context.Context, params querier.ClaimTaskParams) (*querier.AnclaxTask, error) {
			require.NotNil(t, params.LockExpiry)
			require.Equal(t, expectedExpiry, *params.LockExpiry)
			require.False(t, params.HasLabels)
			return &querier.AnclaxTask{
				ID:         1,
				Attributes: apigen.TaskAttributes{},
				Spec:       apigen.TaskSpec{},
				Status:     string(apigen.Pending),
				SerialKey:  nil,
				SerialID:   nil,
			}, nil
		},
	)

	task, err := w.claimTask(context.Background())
	require.NoError(t, err)
	require.NotNil(t, task)
}

func TestRunTaskSkipsWhenLockLostBeforeExecution(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	callCount := 0
	mockModel.EXPECT().RunTransactionWithTx(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			callCount++
			return f(nil, nil)
		},
	).Times(1)

	handler := &stubLifeCycleHandler{
		handleAttributesErr: taskcore.ErrTaskLockLost,
	}
	taskHandler := &stubTaskHandler{}

	w := &Worker{
		model:               mockModel,
		lifeCycleHandler:    handler,
		taskHandler:         taskHandler,
		lockRefreshInterval: 0,
	}

	err := w.runTask(context.Background(), apigen.Task{ID: 1})
	require.NoError(t, err)
	require.Equal(t, 1, callCount)
	require.False(t, taskHandler.called)
}

func TestRunTaskSkipsWhenLockLostAfterFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().RunTransactionWithTx(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			return f(nil, nil)
		},
	).Times(2)

	handler := &stubLifeCycleHandler{
		handleFailedErr: taskcore.ErrTaskLockLost,
	}
	taskHandler := &stubTaskHandler{
		handleFunc: func(context.Context, TaskSpec) error {
			return errors.New("boom")
		},
	}

	w := &Worker{
		model:               mockModel,
		lifeCycleHandler:    handler,
		taskHandler:         taskHandler,
		lockRefreshInterval: 0,
	}

	err := w.runTask(context.Background(), apigen.Task{ID: 2})
	require.NoError(t, err)
	require.True(t, taskHandler.called)
}

func TestRunTaskSkipsWhenLockLostAfterSuccess(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().RunTransactionWithTx(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			return f(nil, nil)
		},
	).Times(2)

	handler := &stubLifeCycleHandler{
		handleCompletedErr: taskcore.ErrTaskLockLost,
	}
	taskHandler := &stubTaskHandler{}

	w := &Worker{
		model:               mockModel,
		lifeCycleHandler:    handler,
		taskHandler:         taskHandler,
		lockRefreshInterval: 0,
	}

	err := w.runTask(context.Background(), apigen.Task{ID: 3})
	require.NoError(t, err)
	require.True(t, taskHandler.called)
}

func TestRunTaskExecutesOutsideTransaction(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	inTx := false
	mockModel.EXPECT().RunTransactionWithTx(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			inTx = true
			err := f(nil, nil)
			inTx = false
			return err
		},
	).Times(2)

	handler := &stubLifeCycleHandler{}
	taskHandler := &stubTaskHandler{
		handleFunc: func(context.Context, TaskSpec) error {
			require.False(t, inTx)
			return nil
		},
	}

	w := &Worker{
		model:               mockModel,
		lifeCycleHandler:    handler,
		taskHandler:         taskHandler,
		lockRefreshInterval: 0,
	}

	err := w.runTask(context.Background(), apigen.Task{ID: 4})
	require.NoError(t, err)
	require.True(t, taskHandler.called)
}

func TestStartLockRefreshUpdatesLock(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	workerID := uuid.New()
	done := make(chan struct{})
	once := false

	mockModel.EXPECT().RefreshTaskLock(gomock.Any(), gomock.AssignableToTypeOf(querier.RefreshTaskLockParams{})).DoAndReturn(
		func(ctx context.Context, params querier.RefreshTaskLockParams) (int32, error) {
			require.True(t, params.WorkerID.Valid)
			require.Equal(t, workerID, params.WorkerID.UUID)
			if !once {
				once = true
				close(done)
			}
			return 1, nil
		},
	).MinTimes(1)

	w := &Worker{
		model:               mockModel,
		workerID:            workerID,
		lockRefreshInterval: 5 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	refreshCancel := w.startLockRefresh(ctx, 99)
	defer refreshCancel()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("lock refresh did not run")
	}
	cancel()
}

func TestStartLockRefreshStopsOnLockLost(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	workerID := uuid.New()
	done := make(chan struct{})

	mockModel.EXPECT().RefreshTaskLock(gomock.Any(), gomock.AssignableToTypeOf(querier.RefreshTaskLockParams{})).DoAndReturn(
		func(ctx context.Context, params querier.RefreshTaskLockParams) (int32, error) {
			close(done)
			return 0, pgx.ErrNoRows
		},
	).Times(1)

	w := &Worker{
		model:               mockModel,
		workerID:            workerID,
		lockRefreshInterval: 5 * time.Millisecond,
	}

	refreshCancel := w.startLockRefresh(context.Background(), 100)
	defer refreshCancel()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("lock refresh did not run")
	}

	time.Sleep(20 * time.Millisecond)
}

type stubLifeCycleHandler struct {
	handleAttributesErr error
	handleFailedErr     error
	handleCompletedErr  error
}

func (s *stubLifeCycleHandler) HandleAttributes(ctx context.Context, tx core.Tx, task apigen.Task) error {
	return s.handleAttributesErr
}

func (s *stubLifeCycleHandler) HandleFailed(ctx context.Context, tx core.Tx, task apigen.Task, err error) error {
	return s.handleFailedErr
}

func (s *stubLifeCycleHandler) HandleCompleted(ctx context.Context, tx core.Tx, task apigen.Task) error {
	return s.handleCompletedErr
}

type stubTaskHandler struct {
	handleFunc   func(context.Context, TaskSpec) error
	onFailedFunc func(context.Context, core.Tx, TaskSpec, int32) error
	called       bool
}

func (s *stubTaskHandler) HandleTask(ctx context.Context, spec TaskSpec) error {
	s.called = true
	if s.handleFunc != nil {
		return s.handleFunc(ctx, spec)
	}
	return nil
}

func (s *stubTaskHandler) RegisterTaskHandler(handler TaskHandler) {}

func (s *stubTaskHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec TaskSpec, taskID int32) error {
	if s.onFailedFunc != nil {
		return s.onFailedFunc(ctx, tx, failedTaskSpec, taskID)
	}
	return nil
}

func TestRegisterWorkerUpsert(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	workerIDStr := workerID.String()
	labels := []string{"billing", "critical"}

	cfg := &config.Config{
		Worker: config.Worker{
			WorkerID: &workerIDStr,
			Labels:   labels,
		},
	}

	mockModel := model.NewMockModelInterface(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	worker, err := NewWorker(globalctx.New(), cfg, mockModel, mockHandler)
	require.NoError(t, err)
	w := worker.(*Worker)

	expectedLabels, err := json.Marshal(labels)
	require.NoError(t, err)

	mockModel.EXPECT().UpsertWorker(context.Background(), gomock.AssignableToTypeOf(querier.UpsertWorkerParams{})).DoAndReturn(
		func(ctx context.Context, params querier.UpsertWorkerParams) (*querier.AnclaxWorker, error) {
			require.Equal(t, workerID, params.ID)
			require.Equal(t, expectedLabels, params.Labels)
			return &querier.AnclaxWorker{}, nil
		},
	)

	err = w.registerWorker(context.Background())
	require.NoError(t, err)
}

func TestHeartbeatUpdatesWorker(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	workerIDStr := workerID.String()

	cfg := &config.Config{
		Worker: config.Worker{
			WorkerID: &workerIDStr,
		},
	}

	mockModel := model.NewMockModelInterface(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	worker, err := NewWorker(globalctx.New(), cfg, mockModel, mockHandler)
	require.NoError(t, err)
	w := worker.(*Worker)

	mockModel.EXPECT().UpdateWorkerHeartbeat(context.Background(), workerID).Return(&querier.AnclaxWorker{}, nil)

	err = w.heartbeat(context.Background())
	require.NoError(t, err)
}

func TestConcurrencyDefaultsToTen(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cfg := &config.Config{}

	mockModel := model.NewMockModelInterface(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	worker, err := NewWorker(globalctx.New(), cfg, mockModel, mockHandler)
	require.NoError(t, err)
	w := worker.(*Worker)

	require.Equal(t, 10, w.concurrency)
	require.Len(t, w.semaphore, 0)
	require.Equal(t, cap(w.semaphore), 10)
}

func TestConcurrencyClampToOne(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	concurrency := 0
	cfg := &config.Config{
		Worker: config.Worker{
			Concurrency: &concurrency,
		},
	}

	mockModel := model.NewMockModelInterface(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	worker, err := NewWorker(globalctx.New(), cfg, mockModel, mockHandler)
	require.NoError(t, err)
	w := worker.(*Worker)

	require.Equal(t, 1, w.concurrency)
	require.Len(t, w.semaphore, 0)
	require.Equal(t, cap(w.semaphore), 1)
}
