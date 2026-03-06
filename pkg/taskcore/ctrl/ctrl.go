package ctrl

import (
	"context"

	"github.com/cloudcarver/anclax/core"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/pkg/errors"
)

// WorkerControlPlane coordinates worker runtime configuration updates.
type WorkerControlPlane struct {
	model  model.ModelInterface
	runner taskgen.TaskRunner
	store  taskcore.TaskStoreInterface
}

func NewWorkerControlPlane(model model.ModelInterface, runner taskgen.TaskRunner, store taskcore.TaskStoreInterface) *WorkerControlPlane {
	return &WorkerControlPlane{model: model, runner: runner, store: store}
}

func collectTaskAndDescendantIDs(ctx context.Context, txm model.ModelInterface, rootTaskID int32) ([]int32, error) {
	descendants, err := txm.ListTaskDescendantIDs(ctx, &rootTaskID)
	if err != nil {
		return nil, err
	}
	ids := make([]int32, 0, len(descendants)+1)
	seen := make(map[int32]struct{}, len(descendants)+1)
	appendUnique := func(id int32) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	appendUnique(rootTaskID)
	for _, id := range descendants {
		appendUnique(id)
	}
	return ids, nil
}

// UpdateWorkerRuntimeConfig enqueues a runtime config update task and waits for all workers to ack it.
func (c *WorkerControlPlane) UpdateWorkerRuntimeConfig(ctx context.Context, req *UpdateWorkerRuntimeConfigRequest) error {
	if req == nil {
		return errors.New("update worker runtime config request cannot be nil")
	}
	taskID, err := RunUpdateWorkerRuntimeConfigTask(ctx, c.runner, req)
	if err != nil {
		return errors.Wrap(err, "run update worker runtime config task")
	}
	if err := c.store.WaitForTask(ctx, taskID); err != nil {
		return errors.Wrap(err, "wait for update worker runtime config task")
	}
	return nil
}

// PauseTask pauses a task and broadcasts a worker pause command, waiting for acknowledgements.
func (c *WorkerControlPlane) PauseTask(ctx context.Context, taskID int32) error {
	if taskID <= 0 {
		return errors.New("pause task requires a positive taskID")
	}

	var broadcastTaskID int32
	err := c.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		taskIDs, err := collectTaskAndDescendantIDs(ctx, txm, taskID)
		if err != nil {
			return errors.Wrap(err, "collect task descendants")
		}
		for _, id := range taskIDs {
			if err := c.store.PauseTaskWithTx(ctx, tx, id); err != nil {
				return errors.Wrap(err, "pause task")
			}
		}
		params := &taskgen.BroadcastPauseTaskParameters{TaskIDs: taskIDs}
		id, err := c.runner.RunBroadcastPauseTaskWithTx(ctx, tx, params)
		if err != nil {
			return errors.Wrap(err, "enqueue broadcast pause task")
		}
		broadcastTaskID = id
		return nil
	})
	if err != nil {
		return err
	}
	if err := c.store.WaitForTask(ctx, broadcastTaskID); err != nil {
		return errors.Wrap(err, "wait for broadcast pause task")
	}
	return nil
}

// CancelTask cancels a task and broadcasts a worker cancel command, waiting for acknowledgements.
func (c *WorkerControlPlane) CancelTask(ctx context.Context, taskID int32) error {
	if taskID <= 0 {
		return errors.New("cancel task requires a positive taskID")
	}
	var broadcastTaskID int32
	err := c.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		taskIDs, err := collectTaskAndDescendantIDs(ctx, txm, taskID)
		if err != nil {
			return errors.Wrap(err, "collect task descendants")
		}
		for _, id := range taskIDs {
			if err := c.store.CancelTaskWithTx(ctx, tx, id); err != nil {
				return errors.Wrap(err, "cancel task")
			}
		}
		params := &taskgen.BroadcastCancelTaskParameters{TaskIDs: taskIDs}
		id, err := c.runner.RunBroadcastCancelTaskWithTx(ctx, tx, params)
		if err != nil {
			return errors.Wrap(err, "enqueue broadcast cancel task")
		}
		broadcastTaskID = id
		return nil
	})
	if err != nil {
		return err
	}
	if err := c.store.WaitForTask(ctx, broadcastTaskID); err != nil {
		return errors.Wrap(err, "wait for broadcast cancel task")
	}
	return nil
}

// ResumeTask resumes a paused task by marking it pending so it can execute again.
func (c *WorkerControlPlane) ResumeTask(ctx context.Context, taskID int32) error {
	if taskID <= 0 {
		return errors.New("resume task requires a positive taskID")
	}
	if err := c.store.ResumeTask(ctx, taskID); err != nil {
		return errors.Wrap(err, "resume task")
	}
	return nil
}

// PauseTaskByUniqueTag pauses a task by unique tag and broadcasts an interrupt request to workers.
func (c *WorkerControlPlane) PauseTaskByUniqueTag(ctx context.Context, uniqueTag string) error {
	if uniqueTag == "" {
		return errors.New("pause task requires unique tag")
	}
	task, err := c.store.GetTaskByUniqueTag(ctx, uniqueTag)
	if err != nil {
		return errors.Wrap(err, "get task by unique tag")
	}
	return c.PauseTask(ctx, task.ID)
}

// CancelTaskByUniqueTag cancels a task by unique tag and broadcasts an interrupt request to workers.
func (c *WorkerControlPlane) CancelTaskByUniqueTag(ctx context.Context, uniqueTag string) error {
	if uniqueTag == "" {
		return errors.New("cancel task requires unique tag")
	}
	task, err := c.store.GetTaskByUniqueTag(ctx, uniqueTag)
	if err != nil {
		return errors.Wrap(err, "get task by unique tag")
	}
	return c.CancelTask(ctx, task.ID)
}

// ResumeTaskByUniqueTag resumes a task by unique tag, allowing it to execute again.
func (c *WorkerControlPlane) ResumeTaskByUniqueTag(ctx context.Context, uniqueTag string) error {
	if uniqueTag == "" {
		return errors.New("resume task requires unique tag")
	}
	task, err := c.store.GetTaskByUniqueTag(ctx, uniqueTag)
	if err != nil {
		return errors.Wrap(err, "get task by unique tag")
	}
	return c.ResumeTask(ctx, task.ID)
}
