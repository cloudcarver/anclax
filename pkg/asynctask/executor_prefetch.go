package asynctask

import (
	"context"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/google/uuid"
	"time"
)

func (e *Executor) ExecutePrefetchTasks(ctx context.Context, task worker.Task, _ *map[string]any) error {
	owner, err := uuid.Parse(e.localWorkerID())
	if err != nil {
		return taskcore.ErrTaskLockLost
	}
	row, err := e.model.GetTaskByID(ctx, task.ID)
	if err != nil {
		return err
	}
	if !row.WorkerID.Valid || row.WorkerID.UUID != owner || row.LeaseVersion != task.LeaseVersion || row.LeaseDurationMs == nil {
		return taskcore.ErrTaskLockLost
	}
	return worker.RunTaskPrefetch(ctx, e.model, task, owner, time.Duration(*row.LeaseDurationMs)*time.Millisecond)
}
