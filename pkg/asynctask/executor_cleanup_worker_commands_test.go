package asynctask

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func expectWorkerCommandCleanup(t *testing.T, runner *taskgen.MockTaskRunner, tx core.Tx, parentTaskID int32) *gomock.Call {
	t.Helper()
	return runner.EXPECT().RunCleanupWorkerCommandTasksWithTx(gomock.Any(), tx, gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ core.Tx, params *taskgen.CleanupWorkerCommandTasksParameters, overrides ...taskcore.TaskOverride) (int32, error) {
			require.Equal(t, parentTaskID, params.ParentTaskID)
			task, err := taskgen.NewCleanupWorkerCommandTasksTask(params, overrides...)
			require.NoError(t, err)
			require.Equal(t, fmt.Sprintf("broadcast:cleanup_worker_commands:%d", parentTaskID), *task.UniqueTag)
			require.Equal(t, int32(0), *task.Attributes.Priority)
			require.Nil(t, task.Attributes.Labels, "any worker must be able to claim cleanup")
			require.Nil(t, task.ParentTaskId, "cancelling the parent must not cancel cleanup")
			return 9001, nil
		},
	)
}

func TestBroadcastDoesNotEnqueueCleanupBeforeFinalization(t *testing.T) {
	for _, command := range []string{"cancel", "pause"} {
		t.Run(command, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			m := model.NewMockModelInterface(ctrl)
			runner := taskgen.NewMockTaskRunner(ctrl)
			executor := &Executor{model: m, runner: runner, now: time.Now, runtimeConfigHeartbeatTTL: 9 * time.Second}
			workerID := uuid.New()
			parent := worker.Task{ID: 41}
			gomock.InOrder(
				m.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{workerID}, nil),
				m.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{workerID}, nil),
				m.EXPECT().GetTaskByUniqueTag(gomock.Any(), gomock.Any()).Return(&querier.AnclaxTask{ID: 101, Status: "pending"}, nil),
				m.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{}, nil),
			)
			var execute func() error
			if command == "cancel" {
				runner.EXPECT().RunCancelTaskOnWorker(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(int32(101), nil)
				execute = func() error {
					return executor.ExecuteBroadcastCancelTask(context.Background(), parent, &taskgen.BroadcastCancelTaskParameters{TaskIDs: []int32{7}, WorkerIDs: []uuid.UUID{workerID}})
				}
			} else {
				runner.EXPECT().RunPauseTaskOnWorker(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(int32(101), nil)
				execute = func() error {
					return executor.ExecuteBroadcastPauseTask(context.Background(), parent, &taskgen.BroadcastPauseTaskParameters{TaskIDs: []int32{7}, WorkerIDs: []uuid.UUID{workerID}})
				}
			}
			var deferred *taskcore.TaskDeferred
			require.ErrorAs(t, execute(), &deferred)
			// The target disappears between attempts. Neither deferral nor handler
			// success enqueues cleanup; the terminal transaction is responsible.
			require.NoError(t, execute())
		})
	}
}

func TestBroadcastTerminalHookEnqueuesCleanupInFinalizationTransaction(t *testing.T) {
	for _, taskType := range []string{taskgen.BroadcastCancelTask, taskgen.BroadcastPauseTask, taskgen.BroadcastUpdateWorkerRuntimeConfig} {
		for _, status := range []apigen.TaskStatus{apigen.Completed, apigen.Failed, apigen.Cancelled} {
			t.Run(taskType+"/"+string(status), func(t *testing.T) {
				ctrl := gomock.NewController(t)
				runner := taskgen.NewMockTaskRunner(ctrl)
				tx := core.NewMockTx(ctrl)
				parent := worker.Task{ID: 42, Spec: apigen.TaskSpec{Type: taskType}}
				expectWorkerCommandCleanup(t, runner, tx, parent.ID)
				// Exercise the generated handler's optional executor hook forwarding.
				handler := taskgen.NewTaskHandler(&Executor{runner: runner}).(worker.TaskTerminalHandler)
				require.NoError(t, handler.OnTaskTerminal(context.Background(), tx, parent, status))
			})
		}
	}
}

func TestBroadcastTerminalHookSkipsNonterminalAndUnrelatedTasks(t *testing.T) {
	ctrl := gomock.NewController(t)
	executor := &Executor{runner: taskgen.NewMockTaskRunner(ctrl)}
	parent := worker.Task{ID: 42, Spec: apigen.TaskSpec{Type: taskgen.BroadcastCancelTask}}
	for _, status := range []apigen.TaskStatus{apigen.Pending, apigen.TaskStatusReady, apigen.TaskStatusRunning, apigen.Paused} {
		require.NoError(t, executor.OnTaskTerminal(context.Background(), nil, parent, status))
	}
	for _, taskType := range []string{taskgen.CleanupWorkerCommandTasks, taskgen.CancelTaskOnWorker, "business-task"} {
		parent.Spec.Type = taskType
		require.ErrorIs(t, executor.OnTaskTerminal(context.Background(), nil, parent, apigen.Completed), worker.ErrUnknownTaskType)
	}
}

func TestBroadcastTerminalHookPropagatesEnqueueFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	runner := taskgen.NewMockTaskRunner(ctrl)
	tx := core.NewMockTx(ctrl)
	executor := &Executor{runner: runner}
	parent := worker.Task{ID: 42, Spec: apigen.TaskSpec{Type: taskgen.BroadcastCancelTask}}
	enqueueErr := fmt.Errorf("database unavailable")
	runner.EXPECT().RunCleanupWorkerCommandTasksWithTx(gomock.Any(), tx, gomock.Any(), gomock.Any(), gomock.Any()).Return(int32(0), enqueueErr)
	require.ErrorIs(t, executor.OnTaskTerminal(context.Background(), tx, parent, apigen.Completed), enqueueErr)
	require.ErrorContains(t, executor.OnTaskTerminal(context.Background(), nil, parent, apigen.Completed), "finalization transaction")
}

type cleanupTerminalHandler struct {
	worker.TaskHandler
	worker.TaskTerminalHandler
}

func TestGeneratedTerminalHookRoutesExternalHandlers(t *testing.T) {
	externalErr := fmt.Errorf("external terminal hook failed")
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"handled externally", nil},
		{"unhandled falls back to executor", worker.ErrUnknownTaskType},
		{"external error aborts", externalErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			runner := taskgen.NewMockTaskRunner(ctrl)
			tx := core.NewMockTx(ctrl)
			parent := worker.Task{ID: 42, Spec: apigen.TaskSpec{Type: taskgen.BroadcastCancelTask}}
			handler := taskgen.NewTaskHandler(&Executor{runner: runner})
			// Existing handlers without the optional hook remain compatible.
			handler.RegisterTaskHandler(worker.NewMockTaskHandler(ctrl))
			external := worker.NewMockTaskTerminalHandler(ctrl)
			handler.RegisterTaskHandler(cleanupTerminalHandler{worker.NewMockTaskHandler(ctrl), external})
			external.EXPECT().OnTaskTerminal(gomock.Any(), tx, parent, apigen.Completed).Return(tc.err)
			if tc.err == worker.ErrUnknownTaskType {
				expectWorkerCommandCleanup(t, runner, tx, parent.ID)
			}
			err := handler.(worker.TaskTerminalHandler).OnTaskTerminal(context.Background(), tx, parent, apigen.Completed)
			if tc.err == externalErr {
				require.ErrorIs(t, err, externalErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCleanupWorkerCommandsWaitsForParentTerminalState(t *testing.T) {
	for _, status := range []apigen.TaskStatus{apigen.Pending, apigen.TaskStatusReady, apigen.TaskStatusRunning, apigen.Paused, apigen.Completed, apigen.Failed, apigen.Cancelled} {
		t.Run(string(status), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			m := model.NewMockModelInterface(ctrl)
			executor := &Executor{model: m}
			m.EXPECT().GetTaskWaitStatusByID(gomock.Any(), int32(42)).Return(&querier.GetTaskWaitStatusByIDRow{ID: 42, Status: string(status)}, nil)
			terminal := status == apigen.Completed || status == apigen.Failed || status == apigen.Cancelled
			if terminal {
				m.EXPECT().CancelWorkerCommandTasksByParentTaskID(gomock.Any(), int32(42)).Return(nil)
			}
			err := executor.ExecuteCleanupWorkerCommandTasks(context.Background(), worker.Task{ID: 43}, &taskgen.CleanupWorkerCommandTasksParameters{ParentTaskID: 42})
			if terminal {
				require.NoError(t, err)
			} else {
				var deferred *taskcore.TaskDeferred
				require.ErrorAs(t, err, &deferred)
				require.Equal(t, time.Second, deferred.Delay)
			}
		})
	}
}

func TestCleanupWorkerCommandsValidationAndDatabaseErrors(t *testing.T) {
	for _, params := range []*taskgen.CleanupWorkerCommandTasksParameters{nil, {}, {ParentTaskID: -1}} {
		require.ErrorIs(t, (&Executor{}).ExecuteCleanupWorkerCommandTasks(context.Background(), worker.Task{}, params), taskcore.ErrFatalTask)
	}
	for _, stage := range []string{"parent", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			m := model.NewMockModelInterface(ctrl)
			executor := &Executor{model: m}
			dbErr := fmt.Errorf("database unavailable")
			if stage == "parent" {
				m.EXPECT().GetTaskWaitStatusByID(gomock.Any(), int32(42)).Return(nil, dbErr)
			} else {
				m.EXPECT().GetTaskWaitStatusByID(gomock.Any(), int32(42)).Return(&querier.GetTaskWaitStatusByIDRow{ID: 42, Status: "completed"}, nil)
				m.EXPECT().CancelWorkerCommandTasksByParentTaskID(gomock.Any(), int32(42)).Return(dbErr)
			}
			require.ErrorIs(t, executor.ExecuteCleanupWorkerCommandTasks(context.Background(), worker.Task{}, &taskgen.CleanupWorkerCommandTasksParameters{ParentTaskID: 42}), dbErr)
		})
	}
}
