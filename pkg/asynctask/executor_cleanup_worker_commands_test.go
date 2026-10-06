package asynctask

import (
	"context"
	"fmt"
	"testing"
	"time"

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

func expectWorkerCommandCleanup(t *testing.T, runner *taskgen.MockTaskRunner, parentTaskID int32) *gomock.Call {
	t.Helper()
	return runner.EXPECT().RunCleanupWorkerCommandTasks(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, params *taskgen.CleanupWorkerCommandTasksParameters, overrides ...taskcore.TaskOverride) (int32, error) {
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

func TestBroadcastWorkerCommandCleanupAfterWorkerDiesBetweenAttempts(t *testing.T) {
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
			// The dead target is filtered before the old cleanup branch. Completion
			// must still durably enqueue cleanup using the parent ID.
			expectWorkerCommandCleanup(t, runner, parent.ID)
			require.NoError(t, execute())
		})
	}
}

func TestBroadcastRetriesWhenCleanupEnqueueFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := model.NewMockModelInterface(ctrl)
	runner := taskgen.NewMockTaskRunner(ctrl)
	executor := &Executor{model: m, runner: runner, now: time.Now}
	parent := worker.Task{ID: 42}
	params := &taskgen.BroadcastCancelTaskParameters{TaskIDs: []int32{7}}
	m.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{}, nil).Times(2)
	enqueueErr := fmt.Errorf("database unavailable")
	runner.EXPECT().RunCleanupWorkerCommandTasks(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(int32(0), enqueueErr)
	require.ErrorIs(t, executor.ExecuteBroadcastCancelTask(context.Background(), parent, params), enqueueErr)
	expectWorkerCommandCleanup(t, runner, parent.ID)
	require.NoError(t, executor.ExecuteBroadcastCancelTask(context.Background(), parent, params))
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
