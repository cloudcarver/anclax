package asynctask

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudcarver/anclax/core"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/pkg/errors"
)

func (e *Executor) OnTaskTerminal(ctx context.Context, tx core.Tx, task worker.Task, status apigen.TaskStatus) error {
	switch task.GetType() {
	case taskgen.BroadcastCancelTask, taskgen.BroadcastPauseTask, taskgen.BroadcastUpdateWorkerRuntimeConfig:
	default:
		return worker.ErrUnknownTaskType
	}
	switch status {
	case apigen.Completed, apigen.Failed, apigen.Cancelled:
	default:
		return nil
	}
	if tx == nil {
		return errors.New("worker command cleanup requires the finalization transaction")
	}
	if e.runner == nil {
		return errors.New("task runner is required for worker command cleanup")
	}
	// Keep cleanup independent of the parent's hierarchy and worker labels so a
	// parent cancellation or a dead target worker cannot strand the cleanup task.
	_, err := e.runner.RunCleanupWorkerCommandTasksWithTx(ctx, tx,
		&taskgen.CleanupWorkerCommandTasksParameters{ParentTaskID: task.ID},
		taskcore.WithUniqueTag(fmt.Sprintf("broadcast:cleanup_worker_commands:%d", task.ID)),
		taskcore.WithPriority(0),
	)
	return errors.Wrap(err, "enqueue worker command cleanup")
}

func (e *Executor) ExecuteCleanupWorkerCommandTasks(ctx context.Context, _ worker.Task, params *taskgen.CleanupWorkerCommandTasksParameters) error {
	if params == nil || params.ParentTaskID <= 0 {
		return errors.Wrap(taskcore.ErrFatalTask, "worker command cleanup requires a positive parentTaskID")
	}
	parent, err := e.model.GetTaskWaitStatusByID(ctx, params.ParentTaskID)
	if err != nil {
		return errors.Wrap(err, "get worker command cleanup parent status")
	}
	switch apigen.TaskStatus(parent.Status) {
	case apigen.Completed, apigen.Failed, apigen.Cancelled:
		// Only direct worker-command children are eligible; terminal history and
		// unrelated business tasks are preserved by the query.
		return errors.Wrap(e.model.CancelWorkerCommandTasksByParentTaskID(ctx, params.ParentTaskID), "cancel unfinished worker commands")
	default:
		// Normal cleanup is committed with the parent's terminal state. Keep a
		// defensive guard for cleanup tasks enqueued directly by other callers.
		return taskcore.DeferTask(time.Second)
	}
}
