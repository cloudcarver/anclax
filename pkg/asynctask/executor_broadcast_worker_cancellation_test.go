package asynctask

import (
	"context"
	"errors"
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

func TestBroadcastCancelsOfflineCommandsBeforeFilteringSnapshot(t *testing.T) {
	for _, command := range []string{"cancel", "pause"} {
		t.Run(command, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			m := model.NewMockModelInterface(ctrl)
			runner := taskgen.NewMockTaskRunner(ctrl)
			executor := &Executor{model: m, runner: runner, now: time.Now}
			workerID := uuid.New()
			parent := worker.Task{ID: 41}
			tag := fmt.Sprintf("broadcast:%s_task:task:41:%s", command, workerID)
			cancelErr := errors.New("database unavailable")
			gomock.InOrder(
				m.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{workerID}, nil),
				m.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{workerID}, nil),
				m.EXPECT().GetTaskByUniqueTag(gomock.Any(), &tag).Return(&querier.AnclaxTask{ID: 101, Status: "pending"}, nil),
				m.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{}, nil),
				m.EXPECT().CancelWorkerCommandTaskByUniqueTag(gomock.Any(), &tag).Return(cancelErr),
				m.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{}, nil),
				m.EXPECT().CancelWorkerCommandTaskByUniqueTag(gomock.Any(), &tag).Return(nil),
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
			require.ErrorIs(t, execute(), cancelErr, "a failed cancellation must retry the parent")
			require.NoError(t, execute(), "only finish after the offline command is cancelled")
		})
	}
}

func TestBroadcastCancelsOfflineCommandsWhileOtherTargetsArePending(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := model.NewMockModelInterface(ctrl)
	executor := &Executor{model: m, now: time.Now}
	dead, alive := uuid.New(), uuid.New()
	snapshot := []uuid.UUID{dead, alive}
	tag := cancelOnWorkerUniqueTag("request", dead)
	uniqueTag := func(id uuid.UUID) string { return cancelOnWorkerUniqueTag("request", id) }
	gomock.InOrder(
		m.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{alive}, nil),
		m.EXPECT().CancelWorkerCommandTaskByUniqueTag(gomock.Any(), &tag).Return(nil),
		m.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{alive}, nil),
		m.EXPECT().GetTaskByUniqueTag(gomock.Any(), gomock.Any()).Return(&querier.AnclaxTask{Status: "pending"}, nil),
		m.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return(snapshot, nil),
		m.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return(snapshot, nil),
	)
	targets, err := executor.aliveSubsetOfSnapshot(context.Background(), snapshot, uniqueTag)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{alive}, targets)
	var deferred *taskcore.TaskDeferred
	require.ErrorAs(t, executor.waitForWorkerCommandTasks(context.Background(), targets, time.Second, uniqueTag), &deferred)
	// A recovered worker keeps the same unique command, already cancelled on a
	// previous attempt. It must not prevent the remaining target from converging.
	m.EXPECT().GetTaskByUniqueTag(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, key *string) (*querier.AnclaxTask, error) {
		status := apigen.Completed
		if *key == tag {
			status = apigen.Cancelled
		}
		return &querier.AnclaxTask{Status: string(status)}, nil
	}).Times(2)
	targets, err = executor.aliveSubsetOfSnapshot(context.Background(), snapshot, uniqueTag)
	require.NoError(t, err)
	require.NoError(t, executor.waitForWorkerCommandTasks(context.Background(), targets, time.Second, uniqueTag))
}
