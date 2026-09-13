package asynctask

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudcarver/anclax/core"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
)

type WorkerControlTaskHandler struct {
	worker worker.WorkerInterface
}

func NewWorkerControlTaskHandler(w worker.WorkerInterface) worker.TaskHandler {
	return &WorkerControlTaskHandler{worker: w}
}

func (h *WorkerControlTaskHandler) HandleTask(ctx context.Context, task worker.Task) error {
	switch task.GetType() {
	case taskgen.ApplyWorkerRuntimeConfigToWorker:
		var params taskgen.ApplyWorkerRuntimeConfigToWorkerParameters
		if err := json.Unmarshal(task.GetPayload(), &params); err != nil {
			return err
		}
		if !h.isTargetWorker(params.WorkerID) {
			return nil
		}
		requestID := ""
		if params.RequestID != nil {
			requestID = *params.RequestID
		}
		h.worker.NotifyRuntimeConfig(requestID)
		return nil
	case taskgen.CancelTaskOnWorker:
		var params taskgen.CancelTaskOnWorkerParameters
		if err := json.Unmarshal(task.GetPayload(), &params); err != nil {
			return err
		}
		if !h.isTargetWorker(params.WorkerID) {
			return nil
		}
		taskIDs := workerControlTaskIDs(task.ID, params.TaskIDs)
		h.worker.InterruptTasks(taskIDs, taskcore.ErrTaskCancelled)
		return waitOrDeferTaskRuntimes(ctx, h.worker, taskIDs, 100*time.Millisecond)
	case taskgen.PauseTaskOnWorker:
		var params taskgen.PauseTaskOnWorkerParameters
		if err := json.Unmarshal(task.GetPayload(), &params); err != nil {
			return err
		}
		if !h.isTargetWorker(params.WorkerID) {
			return nil
		}
		taskIDs := workerControlTaskIDs(task.ID, params.TaskIDs)
		h.worker.InterruptTasks(taskIDs, taskcore.ErrTaskPaused)
		return waitOrDeferTaskRuntimes(ctx, h.worker, taskIDs, 100*time.Millisecond)
	default:
		return worker.ErrUnknownTaskType
	}
}

func (h *WorkerControlTaskHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return worker.ErrUnknownTaskType
}

func (h *WorkerControlTaskHandler) RegisterTaskHandler(handler worker.TaskHandler) {
}

func (h *WorkerControlTaskHandler) isTargetWorker(targetWorkerID uuid.UUID) bool {
	if targetWorkerID == uuid.Nil {
		return true
	}
	workerID, err := uuid.Parse(h.worker.WorkerID())
	if err != nil {
		return false
	}
	return workerID == targetWorkerID
}

func workerControlTaskIDs(controlTaskID int32, taskIDs []int32) []int32 {
	if len(taskIDs) == 0 {
		return nil
	}
	out := make([]int32, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		if taskID == controlTaskID {
			continue
		}
		out = append(out, taskID)
	}
	return out
}

// Production workers expose a non-blocking observation so acknowledgements do
// not occupy the control lane while the interrupted executor is unwinding.
func waitOrDeferTaskRuntimes(ctx context.Context, w worker.WorkerInterface, taskIDs []int32, interval time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if observer, ok := w.(worker.TaskRuntimesObserver); ok {
		if observer.TaskRuntimesActive(taskIDs) {
			return taskcore.DeferTask(interval)
		}
		return nil
	}
	return w.WaitTaskRuntimes(ctx, taskIDs)
}
