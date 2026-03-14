package ctrl

import (
	"context"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/logger"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

var log = logger.NewLogAgent("taskcore.ctrl")

const defaultAliveWorkerHeartbeatTTL = 9 * time.Second

// WorkerControlPlane coordinates worker runtime configuration updates.
type WorkerControlPlane struct {
	model  model.ModelInterface
	runner taskgen.TaskRunner
	store  taskcore.TaskStoreInterface

	now            func() time.Time
	aliveWorkerTTL time.Duration
}

func NewWorkerControlPlane(model model.ModelInterface, runner taskgen.TaskRunner, store taskcore.TaskStoreInterface) *WorkerControlPlane {
	return &WorkerControlPlane{
		model:          model,
		runner:         runner,
		store:          store,
		now:            time.Now,
		aliveWorkerTTL: defaultAliveWorkerHeartbeatTTL,
	}
}

func stringifyWorkerIDs(workerIDs []uuid.UUID) []string {
	if len(workerIDs) == 0 {
		return nil
	}
	out := make([]string, 0, len(workerIDs))
	for _, workerID := range workerIDs {
		if workerID == uuid.Nil {
			continue
		}
		out = append(out, workerID.String())
	}
	return out
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

// StartUpdateWorkerRuntimeConfig snapshots the current alive workers and enqueues a runtime config update task.
func (c *WorkerControlPlane) StartUpdateWorkerRuntimeConfig(ctx context.Context, req *UpdateWorkerRuntimeConfigRequest) (int32, error) {
	if req == nil {
		return 0, errors.New("update worker runtime config request cannot be nil")
	}
	aliveWorkerIDs, err := c.model.ListOnlineWorkerIDs(ctx, c.now().Add(-c.aliveWorkerTTL))
	if err != nil {
		return 0, errors.Wrap(err, "list online worker ids")
	}
	reqCopy := *req
	reqCopy.WorkerIDs = stringifyWorkerIDs(aliveWorkerIDs)
	taskID, err := RunUpdateWorkerRuntimeConfigTask(ctx, c.runner, &reqCopy)
	if err != nil {
		return 0, errors.Wrap(err, "run update worker runtime config task")
	}
	return taskID, nil
}

// WaitForTask waits for the given task to finish.
func (c *WorkerControlPlane) WaitForTask(ctx context.Context, taskID int32) error {
	if taskID <= 0 {
		return errors.New("wait for task requires a positive taskID")
	}
	if err := c.store.WaitForTask(ctx, taskID); err != nil {
		return errors.Wrap(err, "wait for task")
	}
	return nil
}

// UpdateWorkerRuntimeConfig enqueues a runtime config update task and waits for all workers to ack it.
func (c *WorkerControlPlane) UpdateWorkerRuntimeConfig(ctx context.Context, req *UpdateWorkerRuntimeConfigRequest) error {
	taskID, err := c.StartUpdateWorkerRuntimeConfig(ctx, req)
	if err != nil {
		return err
	}
	if err := c.WaitForTask(ctx, taskID); err != nil {
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

		aliveWorkerIDs, err := txm.ListOnlineWorkerIDs(ctx, c.now().Add(-c.aliveWorkerTTL))
		if err != nil {
			return errors.Wrap(err, "list online worker ids")
		}
		if len(aliveWorkerIDs) == 0 {
			return nil
		}

		params := &taskgen.BroadcastPauseTaskParameters{TaskIDs: taskIDs, WorkerIDs: stringifyWorkerIDs(aliveWorkerIDs)}
		id, err := c.runner.RunBroadcastPauseTaskWithTx(ctx, tx, params, taskcore.WithPriority(WorkerControlTaskPriority))
		if err != nil {
			return errors.Wrap(err, "enqueue broadcast pause task")
		}
		broadcastTaskID = id
		return nil
	})
	if err != nil {
		return err
	}
	if broadcastTaskID == 0 {
		log.Info("no alive workers to broadcast pause task to")
		return nil
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

		aliveWorkerIDs, err := txm.ListOnlineWorkerIDs(ctx, c.now().Add(-c.aliveWorkerTTL))
		if err != nil {
			return errors.Wrap(err, "list online worker ids")
		}
		if len(aliveWorkerIDs) == 0 {
			return nil
		}

		params := &taskgen.BroadcastCancelTaskParameters{TaskIDs: taskIDs, WorkerIDs: stringifyWorkerIDs(aliveWorkerIDs)}
		id, err := c.runner.RunBroadcastCancelTaskWithTx(ctx, tx, params, taskcore.WithPriority(WorkerControlTaskPriority))
		if err != nil {
			return errors.Wrap(err, "enqueue broadcast cancel task")
		}
		broadcastTaskID = id
		return nil
	})
	if err != nil {
		return err
	}
	if broadcastTaskID == 0 {
		log.Info("no alive workers to broadcast cancel task to")
		return nil
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
