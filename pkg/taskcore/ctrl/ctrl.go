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

func collectTasksAndDescendantIDs(ctx context.Context, txm model.ModelInterface, rootTaskIDs []int32) ([]int32, error) {
	ids := make([]int32, 0, len(rootTaskIDs))
	seen := make(map[int32]struct{}, len(rootTaskIDs))
	appendUnique := func(id int32) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, rootTaskID := range rootTaskIDs {
		taskIDs, err := collectTaskAndDescendantIDs(ctx, txm, rootTaskID)
		if err != nil {
			return nil, err
		}
		for _, taskID := range taskIDs {
			appendUnique(taskID)
		}
	}
	return ids, nil
}

func normalizeTaskControlLabels(operation string, labels []string) ([]string, error) {
	if len(labels) == 0 {
		return nil, errors.Errorf("%s tasks requires at least one label", operation)
	}
	out := make([]string, 0, len(labels))
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		if label == "" {
			return nil, errors.Errorf("%s tasks requires non-empty labels", operation)
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	return out, nil
}

func uniqueTaskIDs(taskIDs []int32) []int32 {
	if len(taskIDs) == 0 {
		return nil
	}
	out := make([]int32, 0, len(taskIDs))
	seen := make(map[int32]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if _, ok := seen[taskID]; ok {
			continue
		}
		seen[taskID] = struct{}{}
		out = append(out, taskID)
	}
	return out
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

		params := &taskgen.BroadcastPauseTaskParameters{TaskIDs: taskIDs, WorkerIDs: append([]uuid.UUID(nil), aliveWorkerIDs...)}
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

		params := &taskgen.BroadcastCancelTaskParameters{TaskIDs: taskIDs, WorkerIDs: append([]uuid.UUID(nil), aliveWorkerIDs...)}
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

// PauseTasksByLabels pauses tasks whose labels contain every input label and cascades to descendants.
func (c *WorkerControlPlane) PauseTasksByLabels(ctx context.Context, labels []string) error {
	labels, err := normalizeTaskControlLabels("pause", labels)
	if err != nil {
		return err
	}
	var broadcastTaskID int32
	err = c.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		id, err := c.pauseTasksByLabelsInTx(ctx, tx, txm, labels)
		if err != nil {
			return err
		}
		broadcastTaskID = id
		return nil
	})
	if err != nil {
		return err
	}
	if broadcastTaskID == 0 {
		return nil
	}
	if err := c.store.WaitForTask(ctx, broadcastTaskID); err != nil {
		return errors.Wrap(err, "wait for broadcast pause task")
	}
	return nil
}

// PauseTasksByLabelsWithTx pauses matching tasks inside tx and returns the broadcast task ID if one was enqueued.
// Callers that use this to compose label unions should wait for returned broadcast tasks after committing tx.
func (c *WorkerControlPlane) PauseTasksByLabelsWithTx(ctx context.Context, tx core.Tx, labels []string) (int32, error) {
	labels, err := normalizeTaskControlLabels("pause", labels)
	if err != nil {
		return 0, err
	}
	return c.pauseTasksByLabelsWithTx(ctx, tx, labels)
}

func (c *WorkerControlPlane) pauseTasksByLabelsWithTx(ctx context.Context, tx core.Tx, labels []string) (int32, error) {
	if tx == nil {
		return 0, errors.New("pause tasks by labels requires tx")
	}
	txm := c.model.SpawnWithTx(tx)
	return c.pauseTasksByLabelsInTx(ctx, tx, txm, labels)
}

func (c *WorkerControlPlane) pauseTasksByLabelsInTx(ctx context.Context, tx core.Tx, txm model.ModelInterface, labels []string) (int32, error) {
	rootTaskIDs, err := txm.ListTaskIDsByLabels(ctx, labels)
	if err != nil {
		return 0, errors.Wrap(err, "list tasks by labels")
	}
	taskIDs, err := collectTasksAndDescendantIDs(ctx, txm, rootTaskIDs)
	if err != nil {
		return 0, errors.Wrap(err, "collect task descendants by labels")
	}
	if len(taskIDs) == 0 {
		return 0, nil
	}
	for _, id := range taskIDs {
		if err := c.store.PauseTaskWithTx(ctx, tx, id); err != nil {
			return 0, errors.Wrap(err, "pause task")
		}
	}

	aliveWorkerIDs, err := txm.ListOnlineWorkerIDs(ctx, c.now().Add(-c.aliveWorkerTTL))
	if err != nil {
		return 0, errors.Wrap(err, "list online worker ids")
	}
	if len(aliveWorkerIDs) == 0 {
		return 0, nil
	}

	params := &taskgen.BroadcastPauseTaskParameters{TaskIDs: taskIDs, WorkerIDs: append([]uuid.UUID(nil), aliveWorkerIDs...)}
	broadcastTaskID, err := c.runner.RunBroadcastPauseTaskWithTx(ctx, tx, params, taskcore.WithPriority(WorkerControlTaskPriority))
	if err != nil {
		return 0, errors.Wrap(err, "enqueue broadcast pause task")
	}
	return broadcastTaskID, nil
}

// CancelTasksByLabels cancels tasks whose labels contain every input label and cascades to descendants.
func (c *WorkerControlPlane) CancelTasksByLabels(ctx context.Context, labels []string) error {
	labels, err := normalizeTaskControlLabels("cancel", labels)
	if err != nil {
		return err
	}
	var broadcastTaskID int32
	err = c.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		id, err := c.cancelTasksByLabelsInTx(ctx, tx, txm, labels)
		if err != nil {
			return err
		}
		broadcastTaskID = id
		return nil
	})
	if err != nil {
		return err
	}
	if broadcastTaskID == 0 {
		return nil
	}
	if err := c.store.WaitForTask(ctx, broadcastTaskID); err != nil {
		return errors.Wrap(err, "wait for broadcast cancel task")
	}
	return nil
}

// CancelTasksByLabelsWithTx cancels matching tasks inside tx and returns the broadcast task ID if one was enqueued.
// Callers that use this to compose label unions should wait for returned broadcast tasks after committing tx.
func (c *WorkerControlPlane) CancelTasksByLabelsWithTx(ctx context.Context, tx core.Tx, labels []string) (int32, error) {
	labels, err := normalizeTaskControlLabels("cancel", labels)
	if err != nil {
		return 0, err
	}
	return c.cancelTasksByLabelsWithTx(ctx, tx, labels)
}

func (c *WorkerControlPlane) cancelTasksByLabelsWithTx(ctx context.Context, tx core.Tx, labels []string) (int32, error) {
	if tx == nil {
		return 0, errors.New("cancel tasks by labels requires tx")
	}
	txm := c.model.SpawnWithTx(tx)
	return c.cancelTasksByLabelsInTx(ctx, tx, txm, labels)
}

func (c *WorkerControlPlane) cancelTasksByLabelsInTx(ctx context.Context, tx core.Tx, txm model.ModelInterface, labels []string) (int32, error) {
	rootTaskIDs, err := txm.ListTaskIDsByLabels(ctx, labels)
	if err != nil {
		return 0, errors.Wrap(err, "list tasks by labels")
	}
	taskIDs, err := collectTasksAndDescendantIDs(ctx, txm, rootTaskIDs)
	if err != nil {
		return 0, errors.Wrap(err, "collect task descendants by labels")
	}
	if len(taskIDs) == 0 {
		return 0, nil
	}
	for _, id := range taskIDs {
		if err := c.store.CancelTaskWithTx(ctx, tx, id); err != nil {
			return 0, errors.Wrap(err, "cancel task")
		}
	}

	aliveWorkerIDs, err := txm.ListOnlineWorkerIDs(ctx, c.now().Add(-c.aliveWorkerTTL))
	if err != nil {
		return 0, errors.Wrap(err, "list online worker ids")
	}
	if len(aliveWorkerIDs) == 0 {
		return 0, nil
	}

	params := &taskgen.BroadcastCancelTaskParameters{TaskIDs: taskIDs, WorkerIDs: append([]uuid.UUID(nil), aliveWorkerIDs...)}
	broadcastTaskID, err := c.runner.RunBroadcastCancelTaskWithTx(ctx, tx, params, taskcore.WithPriority(WorkerControlTaskPriority))
	if err != nil {
		return 0, errors.Wrap(err, "enqueue broadcast cancel task")
	}
	return broadcastTaskID, nil
}

// ResumeTasksByLabels resumes tasks whose labels contain every input label.
func (c *WorkerControlPlane) ResumeTasksByLabels(ctx context.Context, labels []string) error {
	labels, err := normalizeTaskControlLabels("resume", labels)
	if err != nil {
		return err
	}
	return c.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		return c.resumeTasksByLabelsInTx(ctx, tx, txm, labels)
	})
}

// ResumeTasksByLabelsWithTx resumes matching tasks inside tx.
func (c *WorkerControlPlane) ResumeTasksByLabelsWithTx(ctx context.Context, tx core.Tx, labels []string) error {
	labels, err := normalizeTaskControlLabels("resume", labels)
	if err != nil {
		return err
	}
	return c.resumeTasksByLabelsWithTx(ctx, tx, labels)
}

func (c *WorkerControlPlane) resumeTasksByLabelsWithTx(ctx context.Context, tx core.Tx, labels []string) error {
	if tx == nil {
		return errors.New("resume tasks by labels requires tx")
	}
	txm := c.model.SpawnWithTx(tx)
	return c.resumeTasksByLabelsInTx(ctx, tx, txm, labels)
}

func (c *WorkerControlPlane) resumeTasksByLabelsInTx(ctx context.Context, tx core.Tx, txm model.ModelInterface, labels []string) error {
	taskIDs, err := txm.ListTaskIDsByLabels(ctx, labels)
	if err != nil {
		return errors.Wrap(err, "list tasks by labels")
	}
	for _, taskID := range uniqueTaskIDs(taskIDs) {
		if err := c.store.ResumeTaskWithTx(ctx, tx, taskID); err != nil {
			return errors.Wrap(err, "resume task")
		}
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
