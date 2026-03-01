package ctrl

import (
	"context"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/asynctask"
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

// UpdateWorkerRuntimeConfig enqueues a runtime config update task and waits for all workers to ack it.
func (c *WorkerControlPlane) UpdateWorkerRuntimeConfig(ctx context.Context, params *taskgen.UpdateWorkerRuntimeConfigParameters, overrides ...taskcore.TaskOverride) error {
	taskID, err := asynctask.RunUpdateWorkerRuntimeConfigTask(ctx, c.runner, params, overrides...)
	if err != nil {
		return errors.Wrap(err, "run update worker runtime config task")
	}
	if err := c.store.WaitForTask(ctx, taskID); err != nil {
		return errors.Wrap(err, "wait for update worker runtime config task")
	}
	return nil
}

// PauseTask pauses a task and broadcasts an interrupt request to workers, waiting for acknowledgements.
func (c *WorkerControlPlane) PauseTask(ctx context.Context, params *taskgen.InterruptTaskParameters, overrides ...taskcore.TaskOverride) error {
	if params == nil {
		return errors.New("pause task params cannot be nil")
	}
	if params.TaskID <= 0 {
		return errors.New("pause task requires a positive taskID")
	}

	var cancelTaskID int32
	err := c.model.RunTransactionWithTx(ctx, func(tx core.Tx, _ model.ModelInterface) error {
		if err := c.store.WithTx(tx).PauseTask(ctx, params.TaskID); err != nil {
			return errors.Wrap(err, "pause task")
		}
		id, err := c.runner.RunInterruptTaskWithTx(ctx, tx, params, overrides...)
		if err != nil {
			return errors.Wrap(err, "enqueue pause task cancel")
		}
		cancelTaskID = id
		return nil
	})
	if err != nil {
		return err
	}
	if err := c.store.WaitForTask(ctx, cancelTaskID); err != nil {
		return errors.Wrap(err, "wait for pause task cancel")
	}
	return nil
}

// CancelTask cancels a task and broadcasts an interrupt request to workers, waiting for acknowledgements.
func (c *WorkerControlPlane) CancelTask(ctx context.Context, params *taskgen.InterruptTaskParameters, overrides ...taskcore.TaskOverride) error {
	if params == nil {
		return errors.New("cancel task params cannot be nil")
	}
	if params.TaskID <= 0 {
		return errors.New("cancel task requires a positive taskID")
	}
	var interruptTaskID int32
	err := c.model.RunTransactionWithTx(ctx, func(tx core.Tx, _ model.ModelInterface) error {
		if err := c.store.WithTx(tx).CancelTask(ctx, params.TaskID); err != nil {
			return errors.Wrap(err, "cancel task")
		}
		id, err := c.runner.RunInterruptTaskWithTx(ctx, tx, params, overrides...)
		if err != nil {
			return errors.Wrap(err, "enqueue interrupt task")
		}
		interruptTaskID = id
		return nil
	})
	if err != nil {
		return err
	}
	if err := c.store.WaitForTask(ctx, interruptTaskID); err != nil {
		return errors.Wrap(err, "wait for interrupt task")
	}
	return nil
}
