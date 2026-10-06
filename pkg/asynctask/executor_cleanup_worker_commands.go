package asynctask

import (
	"context"
	"fmt"
	"time"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/pkg/errors"
)

func (e *Executor) enqueueWorkerCommandCleanup(ctx context.Context, parentTaskID int32) error {
	if e.runner == nil {
		return errors.New("task runner is required for worker command cleanup")
	}
	// Keep cleanup independent of the parent's hierarchy and worker labels so a
	// parent cancellation or a dead target worker cannot strand the cleanup task.
	_, err := e.runner.RunCleanupWorkerCommandTasks(ctx,
		&taskgen.CleanupWorkerCommandTasksParameters{ParentTaskID: parentTaskID},
		taskcore.WithUniqueTag(fmt.Sprintf("broadcast:cleanup_worker_commands:%d", parentTaskID)),
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
		// Enqueue happens before the broadcast is finalized. A retrying parent
		// must retain its commands until it reaches a terminal state.
		return taskcore.DeferTask(time.Second)
	}
}
