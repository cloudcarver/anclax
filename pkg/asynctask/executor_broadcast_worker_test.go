package asynctask

import (
	"context"
	stdErrors "errors"
	"testing"
	"time"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestExecuteBroadcastCancelTaskLocalAndRemoteWorker(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	mockLocalWorker := worker.NewMockWorkerInterface(ctrl)

	w1 := uuid.New()
	w2 := uuid.New()
	requestID := "req-cancel"
	fanout := "1ms"
	exec := &Executor{
		model:                     mockModel,
		runner:                    mockRunner,
		localWorker:               mockLocalWorker,
		now:                       time.Now,
		runtimeConfigHeartbeatTTL: 9 * time.Second,
	}

	mockLocalWorker.EXPECT().WorkerID().Return(w1.String())
	mockLocalWorker.EXPECT().InterruptTasks([]int32{101, 102}, taskcore.ErrTaskCancelled)

	mockModel.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{w1, w2}, nil).AnyTimes()

	mockRunner.EXPECT().RunCancelTaskOnWorker(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, params *taskgen.CancelTaskOnWorkerParameters, overrides ...taskcore.TaskOverride) (int32, error) {
			require.Equal(t, w2.String(), params.WorkerID)
			require.Equal(t, []int32{101, 102}, params.TaskIDs)
			task := &apigen.Task{Attributes: apigen.TaskAttributes{}}
			for _, override := range overrides {
				require.NoError(t, override(task))
			}
			require.NotNil(t, task.Attributes.Labels)
			require.Equal(t, []string{"worker:" + w2.String()}, *task.Attributes.Labels)
			require.NotNil(t, task.UniqueTag)
			require.Equal(t, cancelOnWorkerUniqueTag(requestID, w2), *task.UniqueTag)
			return int32(1001), nil
		},
	)

	mockModel.EXPECT().GetTaskByUniqueTag(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, uniqueTag *string) (*querier.AnclaxTask, error) {
			require.Equal(t, cancelOnWorkerUniqueTag(requestID, w2), *uniqueTag)
			return &querier.AnclaxTask{ID: 1001, Status: string(apigen.Completed)}, nil
		},
	)

	err := exec.ExecuteBroadcastCancelTask(context.Background(), &taskgen.BroadcastCancelTaskParameters{
		RequestID:      &requestID,
		TaskIDs:        []int32{101, 102},
		FanoutInterval: &fanout,
	})
	require.NoError(t, err)
}

func TestExecuteBroadcastCancelTaskRemoteFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	w1 := uuid.New()
	requestID := "req-cancel-fail"
	fanout := "1ms"
	exec := &Executor{
		model:                     mockModel,
		runner:                    mockRunner,
		now:                       time.Now,
		runtimeConfigHeartbeatTTL: 9 * time.Second,
	}

	mockModel.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{w1}, nil).AnyTimes()
	mockRunner.EXPECT().RunCancelTaskOnWorker(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(int32(2001), nil)
	mockModel.EXPECT().GetTaskByUniqueTag(gomock.Any(), gomock.Any()).Return(&querier.AnclaxTask{ID: 2001, Status: string(apigen.Failed)}, nil)

	err := exec.ExecuteBroadcastCancelTask(context.Background(), &taskgen.BroadcastCancelTaskParameters{
		RequestID:      &requestID,
		TaskIDs:        []int32{101},
		FanoutInterval: &fanout,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "worker command task failed")
}

func TestExecuteBroadcastPauseTaskLocalAndRemoteWorker(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	mockLocalWorker := worker.NewMockWorkerInterface(ctrl)

	w1 := uuid.New()
	w2 := uuid.New()
	requestID := "req-pause"
	fanout := "1ms"
	exec := &Executor{
		model:                     mockModel,
		runner:                    mockRunner,
		localWorker:               mockLocalWorker,
		now:                       time.Now,
		runtimeConfigHeartbeatTTL: 9 * time.Second,
	}

	mockLocalWorker.EXPECT().WorkerID().Return(w1.String())
	mockLocalWorker.EXPECT().InterruptTasks([]int32{201}, taskcore.ErrTaskPaused)

	mockModel.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{w1, w2}, nil).AnyTimes()

	mockRunner.EXPECT().RunPauseTaskOnWorker(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, params *taskgen.PauseTaskOnWorkerParameters, overrides ...taskcore.TaskOverride) (int32, error) {
			require.Equal(t, w2.String(), params.WorkerID)
			require.Equal(t, []int32{201}, params.TaskIDs)
			task := &apigen.Task{Attributes: apigen.TaskAttributes{}}
			for _, override := range overrides {
				require.NoError(t, override(task))
			}
			require.NotNil(t, task.UniqueTag)
			require.Equal(t, pauseOnWorkerUniqueTag(requestID, w2), *task.UniqueTag)
			return int32(1111), nil
		},
	)

	mockModel.EXPECT().GetTaskByUniqueTag(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, uniqueTag *string) (*querier.AnclaxTask, error) {
			require.Equal(t, pauseOnWorkerUniqueTag(requestID, w2), *uniqueTag)
			return &querier.AnclaxTask{ID: 1111, Status: string(apigen.Completed)}, nil
		},
	)

	err := exec.ExecuteBroadcastPauseTask(context.Background(), &taskgen.BroadcastPauseTaskParameters{
		RequestID:      &requestID,
		TaskIDs:        []int32{201},
		FanoutInterval: &fanout,
	})
	require.NoError(t, err)
}

func TestExecuteWorkerOnlyCommandsReturnFatal(t *testing.T) {
	exec := &Executor{}
	err := exec.ExecuteApplyWorkerRuntimeConfigToWorker(context.Background(), &taskgen.ApplyWorkerRuntimeConfigToWorkerParameters{})
	require.ErrorIs(t, err, taskcore.ErrFatalTask)
	err = exec.ExecuteCancelTaskOnWorker(context.Background(), &taskgen.CancelTaskOnWorkerParameters{})
	require.ErrorIs(t, err, taskcore.ErrFatalTask)
	err = exec.ExecutePauseTaskOnWorker(context.Background(), &taskgen.PauseTaskOnWorkerParameters{})
	require.ErrorIs(t, err, taskcore.ErrFatalTask)
}

func TestExecuteBroadcastUpdateWorkerRuntimeConfigLocalAndRemote(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	mockLocalWorker := worker.NewMockWorkerInterface(ctrl)

	w1 := uuid.New()
	w2 := uuid.New()
	requestID := "req-up"
	fanout := "1ms"
	exec := &Executor{
		model:                     mockModel,
		runner:                    mockRunner,
		localWorker:               mockLocalWorker,
		now:                       time.Now,
		runtimeConfigHeartbeatTTL: 9 * time.Second,
	}

	mockLocalWorker.EXPECT().WorkerID().Return(w1.String())
	mockLocalWorker.EXPECT().NotifyRuntimeConfig(requestID)

	gomock.InOrder(
		mockModel.EXPECT().CreateWorkerRuntimeConfig(gomock.Any(), gomock.Any()).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 7}, nil),
		mockModel.EXPECT().GetLatestWorkerRuntimeConfig(gomock.Any()).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 7}, nil),
		mockModel.EXPECT().ListLaggingAliveWorkers(gomock.Any(), gomock.Any()).Return([]uuid.UUID{w1, w2}, nil),
		mockModel.EXPECT().GetLatestWorkerRuntimeConfig(gomock.Any()).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 7}, nil),
		mockModel.EXPECT().ListLaggingAliveWorkers(gomock.Any(), gomock.Any()).Return([]uuid.UUID{}, nil),
	)

	mockRunner.EXPECT().RunApplyWorkerRuntimeConfigToWorker(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, params *taskgen.ApplyWorkerRuntimeConfigToWorkerParameters, overrides ...taskcore.TaskOverride) (int32, error) {
			require.Equal(t, w2.String(), params.WorkerID)
			require.Equal(t, int64(7), params.Version)
			task := &apigen.Task{Attributes: apigen.TaskAttributes{}}
			for _, override := range overrides {
				require.NoError(t, override(task))
			}
			require.NotNil(t, task.Attributes.Labels)
			require.Equal(t, []string{"worker:" + w2.String()}, *task.Attributes.Labels)
			require.NotNil(t, task.UniqueTag)
			require.Equal(t, applyRuntimeConfigUniqueTag(requestID, w2, 7), *task.UniqueTag)
			return int32(3001), nil
		},
	)

	err := exec.ExecuteBroadcastUpdateWorkerRuntimeConfig(context.Background(), &taskgen.BroadcastUpdateWorkerRuntimeConfigParameters{
		RequestID:      &requestID,
		FanoutInterval: &fanout,
	})
	require.NoError(t, err)
}

func TestExecuteBroadcastCancelTaskInvalidParamsFatal(t *testing.T) {
	exec := &Executor{}
	fanout := "bad"

	err := exec.ExecuteBroadcastCancelTask(context.Background(), nil)
	require.ErrorIs(t, err, taskcore.ErrFatalTask)

	err = exec.ExecuteBroadcastCancelTask(context.Background(), &taskgen.BroadcastCancelTaskParameters{})
	require.ErrorIs(t, err, taskcore.ErrFatalTask)

	err = exec.ExecuteBroadcastCancelTask(context.Background(), &taskgen.BroadcastCancelTaskParameters{TaskIDs: []int32{0}})
	require.ErrorIs(t, err, taskcore.ErrFatalTask)

	err = exec.ExecuteBroadcastCancelTask(context.Background(), &taskgen.BroadcastCancelTaskParameters{TaskIDs: []int32{1}, FanoutInterval: &fanout})
	require.ErrorIs(t, err, taskcore.ErrFatalTask)
}

func TestExecuteBroadcastPauseTaskInvalidParamsFatal(t *testing.T) {
	exec := &Executor{}
	fanout := "bad"

	err := exec.ExecuteBroadcastPauseTask(context.Background(), nil)
	require.ErrorIs(t, err, taskcore.ErrFatalTask)

	err = exec.ExecuteBroadcastPauseTask(context.Background(), &taskgen.BroadcastPauseTaskParameters{})
	require.ErrorIs(t, err, taskcore.ErrFatalTask)

	err = exec.ExecuteBroadcastPauseTask(context.Background(), &taskgen.BroadcastPauseTaskParameters{TaskIDs: []int32{-1}})
	require.ErrorIs(t, err, taskcore.ErrFatalTask)

	err = exec.ExecuteBroadcastPauseTask(context.Background(), &taskgen.BroadcastPauseTaskParameters{TaskIDs: []int32{1}, FanoutInterval: &fanout})
	require.ErrorIs(t, err, taskcore.ErrFatalTask)
}

func TestExecuteBroadcastUpdateWorkerRuntimeConfigInvalidParamsFatal(t *testing.T) {
	exec := &Executor{now: time.Now}
	fanout := "bad"
	tooHigh := int32(101)

	err := exec.ExecuteBroadcastUpdateWorkerRuntimeConfig(context.Background(), nil)
	require.ErrorIs(t, err, taskcore.ErrFatalTask)

	err = exec.ExecuteBroadcastUpdateWorkerRuntimeConfig(context.Background(), &taskgen.BroadcastUpdateWorkerRuntimeConfigParameters{
		Labels:  []string{"a"},
		Weights: []int32{},
	})
	require.ErrorIs(t, err, taskcore.ErrFatalTask)

	err = exec.ExecuteBroadcastUpdateWorkerRuntimeConfig(context.Background(), &taskgen.BroadcastUpdateWorkerRuntimeConfigParameters{
		MaxStrictPercentage: &tooHigh,
	})
	require.ErrorIs(t, err, taskcore.ErrFatalTask)

	err = exec.ExecuteBroadcastUpdateWorkerRuntimeConfig(context.Background(), &taskgen.BroadcastUpdateWorkerRuntimeConfigParameters{
		FanoutInterval: &fanout,
	})
	require.ErrorIs(t, err, taskcore.ErrFatalTask)
}

func TestExecuteBroadcastCancelTaskNoAliveWorkers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	exec := &Executor{model: mockModel, runner: mockRunner, now: time.Now, runtimeConfigHeartbeatTTL: 9 * time.Second}

	mockModel.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{}, nil)
	mockRunner.EXPECT().RunCancelTaskOnWorker(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	err := exec.ExecuteBroadcastCancelTask(context.Background(), &taskgen.BroadcastCancelTaskParameters{TaskIDs: []int32{1}})
	require.NoError(t, err)
}

func TestExecuteBroadcastCancelTaskListAliveWorkersError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	exec := &Executor{model: mockModel, now: time.Now, runtimeConfigHeartbeatTTL: 9 * time.Second}

	errBoom := stdErrors.New("boom")
	mockModel.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return(nil, errBoom)

	err := exec.ExecuteBroadcastCancelTask(context.Background(), &taskgen.BroadcastCancelTaskParameters{TaskIDs: []int32{1}})
	require.Error(t, err)
	require.ErrorContains(t, err, "list online workers")
}

func TestExecuteBroadcastUpdateWorkerRuntimeConfigSuperseded(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	exec := &Executor{model: mockModel, now: time.Now, runtimeConfigHeartbeatTTL: 9 * time.Second}

	mockModel.EXPECT().CreateWorkerRuntimeConfig(gomock.Any(), gomock.Any()).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 1}, nil)
	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(gomock.Any()).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 2}, nil)
	mockModel.EXPECT().ListLaggingAliveWorkers(gomock.Any(), gomock.Any()).Times(0)

	err := exec.ExecuteBroadcastUpdateWorkerRuntimeConfig(context.Background(), &taskgen.BroadcastUpdateWorkerRuntimeConfigParameters{})
	require.NoError(t, err)
}

func TestExecuteBroadcastUpdateWorkerRuntimeConfigCreateError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	exec := &Executor{model: mockModel, now: time.Now, runtimeConfigHeartbeatTTL: 9 * time.Second}

	errBoom := stdErrors.New("boom")
	mockModel.EXPECT().CreateWorkerRuntimeConfig(gomock.Any(), gomock.Any()).Return(nil, errBoom)

	err := exec.ExecuteBroadcastUpdateWorkerRuntimeConfig(context.Background(), &taskgen.BroadcastUpdateWorkerRuntimeConfigParameters{})
	require.Error(t, err)
	require.ErrorContains(t, err, "create worker runtime config")
}

func TestExecuteBroadcastUpdateWorkerRuntimeConfigRunnerRequired(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	exec := &Executor{model: mockModel, now: time.Now, runtimeConfigHeartbeatTTL: 9 * time.Second}

	w1 := uuid.New()
	gomock.InOrder(
		mockModel.EXPECT().CreateWorkerRuntimeConfig(gomock.Any(), gomock.Any()).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 1}, nil),
		mockModel.EXPECT().GetLatestWorkerRuntimeConfig(gomock.Any()).Return(&querier.AnclaxWorkerRuntimeConfig{Version: 1}, nil),
		mockModel.EXPECT().ListLaggingAliveWorkers(gomock.Any(), gomock.Any()).Return([]uuid.UUID{w1}, nil),
	)

	err := exec.ExecuteBroadcastUpdateWorkerRuntimeConfig(context.Background(), &taskgen.BroadcastUpdateWorkerRuntimeConfigParameters{})
	require.Error(t, err)
	require.ErrorContains(t, err, "task runner is required")
}

func TestWaitForWorkerCommandTasksListAliveError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	exec := &Executor{model: mockModel, now: time.Now, runtimeConfigHeartbeatTTL: 9 * time.Second}
	w1 := uuid.New()

	mockModel.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return(nil, stdErrors.New("boom"))

	err := exec.waitForWorkerCommandTasks(context.Background(), []uuid.UUID{w1}, time.Millisecond, func(workerID uuid.UUID) string {
		return "tag"
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "list online workers")
}

func TestWaitForWorkerCommandTasksGetTaskError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	exec := &Executor{model: mockModel, now: time.Now, runtimeConfigHeartbeatTTL: 9 * time.Second}
	w1 := uuid.New()

	gomock.InOrder(
		mockModel.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{w1}, nil),
		mockModel.EXPECT().GetTaskByUniqueTag(gomock.Any(), gomock.Any()).Return(nil, stdErrors.New("db")),
	)

	err := exec.waitForWorkerCommandTasks(context.Background(), []uuid.UUID{w1}, time.Millisecond, func(workerID uuid.UUID) string {
		return "tag"
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "get worker command task")
}

func TestWaitForWorkerCommandTasksContextCanceled(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	exec := &Executor{model: mockModel, now: time.Now, runtimeConfigHeartbeatTTL: 9 * time.Second}
	w1 := uuid.New()

	ctx, cancel := context.WithCancel(context.Background())
	gomock.InOrder(
		mockModel.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{w1}, nil),
		mockModel.EXPECT().GetTaskByUniqueTag(gomock.Any(), gomock.Any()).Return(&querier.AnclaxTask{ID: 1, Status: string(apigen.Pending)}, nil),
	)
	cancel()

	err := exec.waitForWorkerCommandTasks(ctx, []uuid.UUID{w1}, time.Second, func(workerID uuid.UUID) string {
		return "tag"
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestWaitForWorkerCommandTasksMissingThenDead(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterface(ctrl)
	exec := &Executor{model: mockModel, now: time.Now, runtimeConfigHeartbeatTTL: 9 * time.Second}
	w1 := uuid.New()

	gomock.InOrder(
		mockModel.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{w1}, nil),
		mockModel.EXPECT().GetTaskByUniqueTag(gomock.Any(), gomock.Any()).Return(nil, pgx.ErrNoRows),
		mockModel.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{}, nil),
	)

	err := exec.waitForWorkerCommandTasks(context.Background(), []uuid.UUID{w1}, time.Millisecond, func(workerID uuid.UUID) string {
		return "tag"
	})
	require.NoError(t, err)
}

func TestEnqueueHelpersRunnerError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	exec := &Executor{runner: mockRunner}
	wid := uuid.New()
	errBoom := stdErrors.New("enqueue")

	mockRunner.EXPECT().RunApplyWorkerRuntimeConfigToWorker(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(int32(0), errBoom)
	err := exec.enqueueApplyRuntimeConfigToWorker(context.Background(), "r", wid, 1)
	require.Error(t, err)

	mockRunner.EXPECT().RunCancelTaskOnWorker(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(int32(0), errBoom)
	err = exec.enqueueCancelTaskOnWorker(context.Background(), "r", wid, []int32{1})
	require.Error(t, err)

	mockRunner.EXPECT().RunPauseTaskOnWorker(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(int32(0), errBoom)
	err = exec.enqueuePauseTaskOnWorker(context.Background(), "r", wid, []int32{1})
	require.Error(t, err)
}

func TestHelperEdgeCases(t *testing.T) {
	ids, err := normalizeBroadcastTaskIDs([]int32{1, 1, 2})
	require.NoError(t, err)
	require.Equal(t, []int32{1, 2}, ids)

	_, err = normalizeBroadcastTaskIDs(nil)
	require.Error(t, err)

	d, err := parseFanoutInterval(nil)
	require.NoError(t, err)
	require.Equal(t, time.Second, d)

	_, err = parseFanoutInterval(strPtr("-1s"))
	require.Error(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = sleepOrDone(ctx, time.Second)
	require.ErrorIs(t, err, context.Canceled)

	exec := &Executor{}
	err = exec.enqueuePauseTaskOnWorker(context.Background(), "r", uuid.New(), []int32{1})
	require.Error(t, err)
	require.ErrorContains(t, err, "task runner is required")
}

func strPtr(s string) *string { return &s }
