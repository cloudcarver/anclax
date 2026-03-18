package asynctask

import (
	"context"
	"encoding/json"

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
		h.worker.InterruptTasks(params.TaskIDs, taskcore.ErrTaskCancelled)
		return nil
	case taskgen.PauseTaskOnWorker:
		var params taskgen.PauseTaskOnWorkerParameters
		if err := json.Unmarshal(task.GetPayload(), &params); err != nil {
			return err
		}
		if !h.isTargetWorker(params.WorkerID) {
			return nil
		}
		h.worker.InterruptTasks(params.TaskIDs, taskcore.ErrTaskPaused)
		return nil
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
