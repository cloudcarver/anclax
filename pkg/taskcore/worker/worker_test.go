package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/taskcore"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
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
	).Times(2)

	mockTxm.EXPECT().ClaimStrictTask(context.Background(), gomock.AssignableToTypeOf(querier.ClaimStrictTaskParams{})).DoAndReturn(
		func(ctx context.Context, params querier.ClaimStrictTaskParams) (*querier.AnclaxTask, error) {
			require.NotNil(t, params.LockExpiry)
			require.Equal(t, expectedExpiry, *params.LockExpiry)
			require.Equal(t, labels, params.Labels)
			require.True(t, params.HasLabels)
			return nil, pgx.ErrNoRows
		},
	)

	mockTxm.EXPECT().ClaimNormalTaskByGroup(context.Background(), gomock.AssignableToTypeOf(querier.ClaimNormalTaskByGroupParams{})).DoAndReturn(
		func(ctx context.Context, params querier.ClaimNormalTaskByGroupParams) (*querier.AnclaxTask, error) {
			require.NotNil(t, params.LockExpiry)
			require.Equal(t, expectedExpiry, *params.LockExpiry)
			require.Equal(t, labels, params.Labels)
			require.True(t, params.HasLabels)
			require.Equal(t, defaultWeightGroup, params.GroupName)
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
	).Times(2)

	mockTxm.EXPECT().ClaimStrictTask(context.Background(), gomock.AssignableToTypeOf(querier.ClaimStrictTaskParams{})).DoAndReturn(
		func(ctx context.Context, params querier.ClaimStrictTaskParams) (*querier.AnclaxTask, error) {
			require.NotNil(t, params.LockExpiry)
			require.Equal(t, expectedExpiry, *params.LockExpiry)
			require.False(t, params.HasLabels)
			return nil, pgx.ErrNoRows
		},
	)

	mockTxm.EXPECT().ClaimNormalTaskByGroup(context.Background(), gomock.AssignableToTypeOf(querier.ClaimNormalTaskByGroupParams{})).DoAndReturn(
		func(ctx context.Context, params querier.ClaimNormalTaskByGroupParams) (*querier.AnclaxTask, error) {
			require.NotNil(t, params.LockExpiry)
			require.Equal(t, expectedExpiry, *params.LockExpiry)
			require.False(t, params.HasLabels)
			require.Equal(t, defaultWeightGroup, params.GroupName)
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

func TestClaimTaskSkipsStrictWhenStrictCapReached(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	lockTTL := 5 * time.Second
	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTx := core.NewMockTx(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	worker, err := NewWorker(globalctx.New(), &config.Config{
		Worker: config.Worker{
			LockTTL: &lockTTL,
		},
	}, mockModel, mockHandler)
	require.NoError(t, err)
	w := worker.(*Worker)
	w.strictCap = 1
	w.strictInFlight = 1

	mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			return f(mockTx, mockTxm)
		},
	).Times(1)

	mockTxm.EXPECT().ClaimNormalTaskByGroup(context.Background(), gomock.AssignableToTypeOf(querier.ClaimNormalTaskByGroupParams{})).DoAndReturn(
		func(ctx context.Context, params querier.ClaimNormalTaskByGroupParams) (*querier.AnclaxTask, error) {
			require.Equal(t, defaultWeightGroup, params.GroupName)
			return &querier.AnclaxTask{
				ID:         10,
				Attributes: apigen.TaskAttributes{},
				Spec:       apigen.TaskSpec{},
				Status:     string(apigen.Pending),
			}, nil
		},
	).Times(1)

	task, err := w.claimTask(context.Background())
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, int32(10), task.ID)
}

func TestClaimTaskTriesWeightedGroupsUntilMatch(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	lockTTL := 5 * time.Second
	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTx := core.NewMockTx(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	worker, err := NewWorker(globalctx.New(), &config.Config{
		Worker: config.Worker{
			LockTTL: &lockTTL,
		},
	}, mockModel, mockHandler)
	require.NoError(t, err)
	w := worker.(*Worker)
	w.strictCap = 0
	w.weightedLabels = []string{"w1", "w2"}
	w.normalClaimWheel = []string{"w1", "w1", "w2", defaultWeightGroup}
	w.normalClaimWheelCursor = 0

	mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			return f(mockTx, mockTxm)
		},
	).Times(2)

	call := 0
	mockTxm.EXPECT().ClaimNormalTaskByGroup(context.Background(), gomock.AssignableToTypeOf(querier.ClaimNormalTaskByGroupParams{})).DoAndReturn(
		func(ctx context.Context, params querier.ClaimNormalTaskByGroupParams) (*querier.AnclaxTask, error) {
			call++
			require.Equal(t, []string{"w1", "w2"}, params.WeightedLabels)
			if call == 1 {
				require.Equal(t, "w1", params.GroupName)
				return nil, pgx.ErrNoRows
			}
			require.Equal(t, "w2", params.GroupName)
			return &querier.AnclaxTask{
				ID:         11,
				Attributes: apigen.TaskAttributes{},
				Spec:       apigen.TaskSpec{},
				Status:     string(apigen.Pending),
			}, nil
		},
	).Times(2)

	task, err := w.claimTask(context.Background())
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, int32(11), task.ID)
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
			require.Equal(t, int64(0), params.AppliedConfigVersion)
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

func TestStrictCapForPercentage(t *testing.T) {
	require.Equal(t, 0, strictCapForPercentage(10, 0))
	require.Equal(t, 1, strictCapForPercentage(10, 1))
	require.Equal(t, 3, strictCapForPercentage(10, 21))
	require.Equal(t, 10, strictCapForPercentage(10, 100))
}

func TestTryReserveStrictInFlightIncrementsSaturationMetric(t *testing.T) {
	w := &Worker{
		strictCap:      1,
		strictInFlight: 1,
	}
	before := testutil.ToFloat64(metrics.WorkerStrictSaturationTotal)
	ok := w.tryReserveStrictInFlight()
	after := testutil.ToFloat64(metrics.WorkerStrictSaturationTotal)
	require.False(t, ok)
	require.Equal(t, before+1, after)
}

func TestApplyRuntimeConfigUpdatesStrictMetrics(t *testing.T) {
	w := &Worker{concurrency: 10}
	percentage := int32(25)
	w.applyRuntimeConfigLocked(12, runtimeConfigPayload{
		MaxStrictPercentage: &percentage,
		LabelWeights: map[string]int32{
			"default": 1,
			"w1":      2,
		},
	})

	require.Equal(t, float64(3), testutil.ToFloat64(metrics.WorkerStrictCap))
	require.Equal(t, float64(0), testutil.ToFloat64(metrics.WorkerStrictInFlight))
	require.Equal(t, float64(12), testutil.ToFloat64(metrics.WorkerRuntimeConfigVersion))
}

func TestBuildClaimWheelUsesWeightsAndSortOrder(t *testing.T) {
	wheel := buildClaimWheel(map[string]int32{
		"w2":               1,
		"w1":               2,
		defaultWeightGroup: 1,
	})
	require.Equal(t, []string{defaultWeightGroup, "w1", "w1", "w2"}, wheel)
}

func TestApplyRuntimeConfigBuildsWheelAndCap(t *testing.T) {
	w := &Worker{concurrency: 10}
	percentage := int32(30)
	w.applyRuntimeConfigLocked(5, runtimeConfigPayload{
		MaxStrictPercentage: &percentage,
		LabelWeights: map[string]int32{
			"w1": 5,
			"w2": 1,
		},
	})

	require.Equal(t, int64(5), w.runtimeConfigVersion)
	require.Equal(t, int32(30), w.maxStrictPercentage)
	require.Equal(t, 3, w.strictCap)
	require.Equal(t, []string{"w1", "w2"}, w.weightedLabels)
	require.Contains(t, w.normalClaimWheel, defaultWeightGroup)
}

func TestNextNormalClaimGroups(t *testing.T) {
	w := &Worker{
		weightedLabels:   []string{"w1", "w2"},
		normalClaimWheel: []string{"w1", "w1", "w2", defaultWeightGroup},
	}

	order1, labels1 := w.nextNormalClaimGroups()
	order2, labels2 := w.nextNormalClaimGroups()
	require.Equal(t, []string{"w1", "w2", defaultWeightGroup}, order1)
	require.Equal(t, []string{"w1", "w2"}, labels1)
	require.Equal(t, []string{"w1", "w2"}, labels2)
	require.Equal(t, []string{"w1", "w2", defaultWeightGroup}, order2)
}

func TestApplyRuntimeConfigBuildsWheelFromDefaultKey(t *testing.T) {
	w := &Worker{concurrency: 8}
	w.applyRuntimeConfigLocked(3, runtimeConfigPayload{
		LabelWeights: map[string]int32{
			"default": 4,
			"ops":     2,
		},
	})

	require.Equal(t, int64(3), w.runtimeConfigVersion)
	require.Contains(t, w.normalClaimWheel, defaultWeightGroup)
	require.NotContains(t, w.weightedLabels, defaultWeightGroup)
	require.Equal(t, []string{"ops"}, w.weightedLabels)
}

func TestHandleRuntimeConfigNotificationIgnoresUnknownOp(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	w := &Worker{
		model: mockModel,
	}
	err := w.handleRuntimeConfigNotification(context.Background(), `{"op":"noop","params":{"request_id":"x","version":1}}`)
	require.NoError(t, err)
}

func TestHandleRuntimeConfigNotificationRejectsInvalidJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	w := &Worker{
		model: model.NewMockModelInterface(ctrl),
	}
	err := w.handleRuntimeConfigNotification(context.Background(), `{`)
	require.Error(t, err)
}

func TestRefreshRuntimeConfigNoRowsAcksCurrentVersion(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	workerID := uuid.New()
	requestID := "req-1"
	mockModel := model.NewMockModelInterface(ctrl)

	w := &Worker{
		model:    mockModel,
		workerID: workerID,
	}
	w.applyRuntimeConfigLocked(9, runtimeConfigPayload{
		LabelWeights: map[string]int32{defaultWeightGroup: 1},
	})

	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(nil, pgx.ErrNoRows)
	mockModel.EXPECT().NotifyWorkerRuntimeConfigAck(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, payload string) error {
			var ack runtimeConfigAckNotification
			require.NoError(t, json.Unmarshal([]byte(payload), &ack))
			require.Equal(t, "ack", ack.Op)
			require.Equal(t, requestID, ack.Params.RequestID)
			require.Equal(t, workerID.String(), ack.Params.WorkerID)
			require.Equal(t, int64(9), ack.Params.AppliedVersion)
			return nil
		},
	)

	err := w.refreshRuntimeConfig(ctx, requestID)
	require.NoError(t, err)
}

func TestRefreshRuntimeConfigStaleVersionAcksWithoutApply(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	workerID := uuid.New()
	requestID := "req-2"
	mockModel := model.NewMockModelInterface(ctrl)

	w := &Worker{
		model:    mockModel,
		workerID: workerID,
	}
	w.applyRuntimeConfigLocked(5, runtimeConfigPayload{
		LabelWeights: map[string]int32{defaultWeightGroup: 1},
	})

	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(&querier.AnclaxWorkerRuntimeConfig{
		Version: 5,
	}, nil)
	mockModel.EXPECT().NotifyWorkerRuntimeConfigAck(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, payload string) error {
			var ack runtimeConfigAckNotification
			require.NoError(t, json.Unmarshal([]byte(payload), &ack))
			require.Equal(t, requestID, ack.Params.RequestID)
			require.Equal(t, int64(5), ack.Params.AppliedVersion)
			return nil
		},
	)

	err := w.refreshRuntimeConfig(ctx, requestID)
	require.NoError(t, err)
}

func TestRefreshRuntimeConfigAppliesAndUpdatesWorkerVersion(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	workerID := uuid.New()
	requestID := "req-3"
	mockModel := model.NewMockModelInterface(ctrl)

	w := &Worker{
		model:       mockModel,
		workerID:    workerID,
		concurrency: 10,
	}
	w.applyRuntimeConfigLocked(1, runtimeConfigPayload{
		LabelWeights: map[string]int32{defaultWeightGroup: 1},
	})

	payload := `{"maxStrictPercentage":20,"labelWeights":{"default":2,"w1":5}}`
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(&querier.AnclaxWorkerRuntimeConfig{
		Version: 3,
		Payload: json.RawMessage(payload),
	}, nil)
	mockModel.EXPECT().UpdateWorkerAppliedConfigVersion(ctx, querier.UpdateWorkerAppliedConfigVersionParams{
		ID:                   workerID,
		AppliedConfigVersion: int64(3),
	}).Return(nil)
	mockModel.EXPECT().NotifyWorkerRuntimeConfigAck(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, payload string) error {
			var ack runtimeConfigAckNotification
			require.NoError(t, json.Unmarshal([]byte(payload), &ack))
			require.Equal(t, requestID, ack.Params.RequestID)
			require.Equal(t, int64(3), ack.Params.AppliedVersion)
			return nil
		},
	)

	err := w.refreshRuntimeConfig(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, int64(3), w.currentRuntimeConfigVersion())
	require.Equal(t, 2, w.strictCap) // 20% of 10 with ceil.
}

func TestRuntimeListenDSNFromConfig(t *testing.T) {
	dsn := "postgres://u:p@h:5432/db?sslmode=disable"
	require.Equal(t, dsn, runtimeListenDSNFromConfig(&config.Config{
		Pg: config.Pg{DSN: &dsn},
	}))

	assembled := runtimeListenDSNFromConfig(&config.Config{
		Pg: config.Pg{
			User:     "postgres",
			Password: "postgres",
			Host:     "localhost",
			Port:     5432,
			Db:       "postgres",
			SSLMode:  "disable",
		},
	})
	require.True(t, strings.Contains(assembled, "postgres://postgres:postgres@localhost:5432/postgres"))
	require.True(t, strings.Contains(assembled, "sslmode=disable"))
}

func TestClaimTaskReleasesStrictInFlightOnStrictClaimError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	lockTTL := 5 * time.Second
	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTx := core.NewMockTx(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	worker, err := NewWorker(globalctx.New(), &config.Config{
		Worker: config.Worker{LockTTL: &lockTTL},
	}, mockModel, mockHandler)
	require.NoError(t, err)
	w := worker.(*Worker)
	w.strictCap = 1
	w.strictInFlight = 0

	mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			return f(mockTx, mockTxm)
		},
	).Times(1)
	mockTxm.EXPECT().ClaimStrictTask(context.Background(), gomock.AssignableToTypeOf(querier.ClaimStrictTaskParams{})).
		Return(nil, errors.New("claim strict failed")).Times(1)

	task, err := w.claimTask(context.Background())
	require.Nil(t, task)
	require.ErrorContains(t, err, "claim strict failed")
	require.Equal(t, 0, w.strictInFlight)
}

func TestClaimTaskReturnsNormalClaimError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	lockTTL := 5 * time.Second
	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTx := core.NewMockTx(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	worker, err := NewWorker(globalctx.New(), &config.Config{
		Worker: config.Worker{LockTTL: &lockTTL},
	}, mockModel, mockHandler)
	require.NoError(t, err)
	w := worker.(*Worker)
	w.strictCap = 0

	mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			return f(mockTx, mockTxm)
		},
	).Times(1)
	mockTxm.EXPECT().ClaimNormalTaskByGroup(context.Background(), gomock.AssignableToTypeOf(querier.ClaimNormalTaskByGroupParams{})).
		Return(nil, errors.New("claim normal failed")).Times(1)

	task, err := w.claimTask(context.Background())
	require.Nil(t, task)
	require.ErrorContains(t, err, "claim normal failed")
}

func TestRefreshRuntimeConfigPropagatesGetLatestError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(context.Background()).
		Return(nil, errors.New("read latest failed"))

	w := &Worker{
		model:    mockModel,
		workerID: uuid.New(),
	}
	err := w.refreshRuntimeConfig(context.Background(), "")
	require.ErrorContains(t, err, "read latest failed")
}

func TestRefreshRuntimeConfigRejectsInvalidPayload(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(context.Background()).
		Return(&querier.AnclaxWorkerRuntimeConfig{
			Version: 2,
			Payload: []byte("{"),
		}, nil)

	w := &Worker{
		model:       mockModel,
		workerID:    uuid.New(),
		concurrency: 10,
	}
	w.applyRuntimeConfigLocked(1, runtimeConfigPayload{
		LabelWeights: map[string]int32{defaultWeightGroup: 1},
	})

	err := w.refreshRuntimeConfig(context.Background(), "")
	require.ErrorContains(t, err, "unmarshal worker runtime config payload")
}

func TestRefreshRuntimeConfigPropagatesUpdateAppliedVersionError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).
		Return(&querier.AnclaxWorkerRuntimeConfig{
			Version: 2,
			Payload: []byte(`{"labelWeights":{"default":1}}`),
		}, nil)
	mockModel.EXPECT().UpdateWorkerAppliedConfigVersion(ctx, querier.UpdateWorkerAppliedConfigVersionParams{
		ID:                   workerID,
		AppliedConfigVersion: 2,
	}).Return(errors.New("update applied version failed"))

	w := &Worker{
		model:       mockModel,
		workerID:    workerID,
		concurrency: 10,
	}
	w.applyRuntimeConfigLocked(1, runtimeConfigPayload{
		LabelWeights: map[string]int32{defaultWeightGroup: 1},
	})

	err := w.refreshRuntimeConfig(ctx, "")
	require.ErrorContains(t, err, "update worker applied config version")
}

func TestRefreshRuntimeConfigNoRowsWithoutRequestIDSkipsAck(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(context.Background()).Return(nil, pgx.ErrNoRows)

	w := &Worker{
		model:    mockModel,
		workerID: uuid.New(),
	}
	err := w.refreshRuntimeConfig(context.Background(), "")
	require.NoError(t, err)
}

func TestRefreshRuntimeConfigStaleWithoutRequestIDSkipsAck(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(context.Background()).
		Return(&querier.AnclaxWorkerRuntimeConfig{Version: 2}, nil)

	w := &Worker{
		model:    mockModel,
		workerID: uuid.New(),
	}
	w.applyRuntimeConfigLocked(3, runtimeConfigPayload{
		LabelWeights: map[string]int32{defaultWeightGroup: 1},
	})

	err := w.refreshRuntimeConfig(context.Background(), "")
	require.NoError(t, err)
}

func TestRefreshRuntimeConfigNoRowsAckErrorPropagates(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	requestID := "req-ack-fail"
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(ctx).Return(nil, pgx.ErrNoRows)
	mockModel.EXPECT().NotifyWorkerRuntimeConfigAck(ctx, gomock.Any()).
		Return(errors.New("ack failed"))

	w := &Worker{
		model:    mockModel,
		workerID: uuid.New(),
	}
	w.applyRuntimeConfigLocked(4, runtimeConfigPayload{
		LabelWeights: map[string]int32{defaultWeightGroup: 1},
	})

	err := w.refreshRuntimeConfig(ctx, requestID)
	require.ErrorContains(t, err, "notify runtime config ack")
}

func TestMarkOfflineCallsModel(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().MarkWorkerOffline(context.Background(), workerID).Return(nil)

	w := &Worker{
		model:    mockModel,
		workerID: workerID,
	}
	require.NoError(t, w.markOffline(context.Background()))
}

func TestClaimTaskByIDNoRowsReturnsNil(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	lockTTL := 5 * time.Second
	mockModel := model.NewMockModelInterface(ctrl)
	mockTxm := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTx := core.NewMockTx(ctrl)
	mockHandler := NewMockTaskHandler(ctrl)

	worker, err := NewWorker(globalctx.New(), &config.Config{
		Worker: config.Worker{LockTTL: &lockTTL},
	}, mockModel, mockHandler)
	require.NoError(t, err)
	w := worker.(*Worker)

	mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			return f(mockTx, mockTxm)
		},
	).Times(1)
	mockTxm.EXPECT().ClaimTaskByID(context.Background(), gomock.AssignableToTypeOf(querier.ClaimTaskByIDParams{})).
		Return(nil, pgx.ErrNoRows).Times(1)

	task, err := w.claimTaskByID(context.Background(), 123)
	require.NoError(t, err)
	require.Nil(t, task)
}

func TestRunTaskPropagatesInvalidTimeoutError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	taskHandler := &stubTaskHandler{}
	handler := &stubLifeCycleHandler{}
	w := &Worker{
		model:               mockModel,
		taskHandler:         taskHandler,
		lifeCycleHandler:    handler,
		lockRefreshInterval: 0,
	}

	invalid := "nope"
	err := w.runTask(context.Background(), apigen.Task{
		ID: 1,
		Attributes: apigen.TaskAttributes{
			Timeout: &invalid,
		},
	})
	require.ErrorContains(t, err, "failed to parse timeout")
}
