package worker

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/taskcore"
	"github.com/cloudcarver/anclax/pkg/taskcore/types"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

var log = logger.NewLogAgent("worker")

type Worker struct {
	model model.ModelInterface

	lifeCycleHandler TaskLifeCycleHandlerInterface

	globalCtx *globalctx.GlobalContext

	taskHandler TaskHandler

	pollInterval time.Duration

	workerID            uuid.UUID
	labels              []string
	hasLabels           bool
	labelsJSON          json.RawMessage
	heartbeatInterval   time.Duration
	lockTTL             time.Duration
	lockRefreshInterval time.Duration

	now func() time.Time
}

func NewWorker(globalCtx *globalctx.GlobalContext, cfg *config.Config, model model.ModelInterface, taskHandler TaskHandler) (WorkerInterface, error) {
	pollInterval := 1 * time.Second
	if cfg.Worker.PollInterval != nil {
		pollInterval = *cfg.Worker.PollInterval
	}

	heartbeatInterval := 3 * time.Second
	if cfg.Worker.HeartbeatInterval != nil {
		heartbeatInterval = *cfg.Worker.HeartbeatInterval
	}

	lockTTL := 9 * time.Second
	if cfg.Worker.LockTTL != nil {
		lockTTL = *cfg.Worker.LockTTL
	}

	lockRefreshInterval := heartbeatInterval
	if cfg.Worker.LockRefreshInterval != nil {
		lockRefreshInterval = *cfg.Worker.LockRefreshInterval
	}

	workerID := uuid.New()
	if cfg.Worker.WorkerID != nil {
		parsed, err := uuid.Parse(*cfg.Worker.WorkerID)
		if err != nil {
			return nil, errors.Wrap(err, "invalid workerId")
		}
		workerID = parsed
	}

	labels := cfg.Worker.Labels
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return nil, errors.Wrap(err, "marshal worker labels")
	}

	w := &Worker{
		model:               model,
		lifeCycleHandler:    NewTaskLifeCycleHandler(model, taskHandler, workerID),
		globalCtx:           globalCtx,
		taskHandler:         taskHandler,
		pollInterval:        pollInterval,
		workerID:            workerID,
		labels:              labels,
		hasLabels:           len(labels) > 0,
		labelsJSON:          labelsJSON,
		heartbeatInterval:   heartbeatInterval,
		lockTTL:             lockTTL,
		lockRefreshInterval: lockRefreshInterval,
		now:                 time.Now,
	}

	return w, nil
}

func (w *Worker) Start() {
	if err := w.registerWorker(w.globalCtx.Context()); err != nil {
		log.Error("failed to register worker", zap.Error(err))
		return
	}

	go w.heartbeatLoop()

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.globalCtx.Context().Done():
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = w.markOffline(ctx)
			return
		case <-ticker.C:
			go func() {
				metrics.WorkerGoroutines.Inc()
				defer metrics.WorkerGoroutines.Dec()
				if err := w.pullAndRun(w.globalCtx.Context()); err != nil {
					metrics.RunTaskErrors.Inc()
					log.Error("error running task", zap.Error(err))
				}
			}()
		}
	}
}

func (w *Worker) pullAndRun(parentCtx context.Context) error {
	task, err := w.claimTask(parentCtx)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}
	metrics.PulledTasks.Inc()
	return w.runTask(parentCtx, *task)
}

func (w *Worker) RunTask(ctx context.Context, taskID int32) error {
	task, err := w.claimTaskByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}
	return w.runTask(ctx, *task)
}

func (w *Worker) runTask(_ctx context.Context, task apigen.Task) error {
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)

	if task.Attributes.Timeout == nil {
		ctx, cancel = context.WithCancel(_ctx)
	} else {
		timeout, err := time.ParseDuration(*task.Attributes.Timeout)
		if err != nil {
			return errors.Wrap(err, "failed to parse timeout")
		}
		ctx, cancel = context.WithTimeout(_ctx, timeout)
	}
	defer cancel()

	refreshCancel := w.startLockRefresh(ctx, task.ID)
	defer refreshCancel()

	log.Info("executing task", zap.Int32("task_id", task.ID), zap.Any("task", task))

	if err := w.runHandleAttributes(ctx, task); err != nil {
		if errors.Is(err, taskcore.ErrTaskLockLost) {
			log.Warn("task lock lost before execution", zap.Int32("task_id", task.ID))
			return nil
		}
		return errors.Wrap(err, "failed to handle attributes")
	}

	err := w.taskHandler.HandleTask(ctx, &task.Spec)
	if err != nil { // handle failed
		if err != taskcore.ErrRetryTaskWithoutErrorEvent {
			log.Error("error executing task", zap.Int32("task_id", task.ID), zap.Error(err))
		}
		if err := w.runHandleFailed(ctx, task, err); err != nil {
			if errors.Is(err, taskcore.ErrTaskLockLost) {
				log.Warn("task lock lost after failure", zap.Int32("task_id", task.ID))
				return nil
			}
			return errors.Wrap(err, "failed to handle failed task")
		}
	} else { // handle completed
		if err := w.runHandleCompleted(ctx, task); err != nil {
			if errors.Is(err, taskcore.ErrTaskLockLost) {
				log.Warn("task lock lost after completion", zap.Int32("task_id", task.ID))
				return nil
			}
			log.Error("error handling completed task", zap.Int32("task_id", task.ID), zap.Error(err))
			return errors.Wrap(err, "failed to handle completed task")
		}
		log.Info("task completed", zap.Int32("task_id", task.ID))
	}
	return nil
}

func (w *Worker) RegisterTaskHandler(handler TaskHandler) {
	w.taskHandler.RegisterTaskHandler(handler)
}

func (w *Worker) claimTask(ctx context.Context) (*apigen.Task, error) {
	var task *apigen.Task
	lockExpiry := w.now().Add(-w.lockTTL)
	workerID := w.workerIDParam()
	if err := w.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		qtask, err := txm.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerID,
			LockExpiry: &lockExpiry,
			Labels:     w.labels,
			HasLabels:  w.hasLabels,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		converted := types.TaskToAPI(qtask)
		task = &converted
		return nil
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (w *Worker) claimTaskByID(ctx context.Context, taskID int32) (*apigen.Task, error) {
	var task *apigen.Task
	lockExpiry := w.now().Add(-w.lockTTL)
	workerID := w.workerIDParam()
	if err := w.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		qtask, err := txm.ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{
			ID:         taskID,
			WorkerID:   workerID,
			LockExpiry: &lockExpiry,
			Labels:     w.labels,
			HasLabels:  w.hasLabels,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		converted := types.TaskToAPI(qtask)
		task = &converted
		return nil
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (w *Worker) runHandleAttributes(ctx context.Context, task apigen.Task) error {
	return w.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		return w.lifeCycleHandler.HandleAttributes(ctx, tx, task)
	})
}

func (w *Worker) runHandleFailed(ctx context.Context, task apigen.Task, err error) error {
	return w.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		return w.lifeCycleHandler.HandleFailed(ctx, tx, task, err)
	})
}

func (w *Worker) runHandleCompleted(ctx context.Context, task apigen.Task) error {
	return w.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		return w.lifeCycleHandler.HandleCompleted(ctx, tx, task)
	})
}

func (w *Worker) startLockRefresh(ctx context.Context, taskID int32) context.CancelFunc {
	if w.lockRefreshInterval <= 0 {
		return func() {}
	}
	refreshCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(w.lockRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-ticker.C:
				if _, err := w.model.RefreshTaskLock(refreshCtx, querier.RefreshTaskLockParams{
					ID:       taskID,
					WorkerID: w.workerIDParam(),
				}); err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						log.Warn("task lock refresh failed, lock lost", zap.Int32("task_id", taskID))
						return
					}
					log.Error("failed to refresh task lock", zap.Int32("task_id", taskID), zap.Error(err))
				}
			}
		}
	}()
	return cancel
}

func (w *Worker) heartbeatLoop() {
	ticker := time.NewTicker(w.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.globalCtx.Context().Done():
			return
		case <-ticker.C:
			if err := w.heartbeat(w.globalCtx.Context()); err != nil {
				log.Error("failed to update worker heartbeat", zap.Error(err))
			}
		}
	}
}

func (w *Worker) registerWorker(ctx context.Context) error {
	_, err := w.model.UpsertWorker(ctx, querier.UpsertWorkerParams{
		ID:     w.workerID,
		Labels: w.labelsJSON,
	})
	return err
}

func (w *Worker) heartbeat(ctx context.Context) error {
	_, err := w.model.UpdateWorkerHeartbeat(ctx, w.workerID)
	return err
}

func (w *Worker) markOffline(ctx context.Context) error {
	return w.model.MarkWorkerOffline(ctx, w.workerID)
}

func (w *Worker) workerIDParam() uuid.NullUUID {
	return uuid.NullUUID{UUID: w.workerID, Valid: true}
}
