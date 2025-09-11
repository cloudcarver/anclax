package worker

import (
	"context"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/taskcore"
	"github.com/cloudcarver/anclax/pkg/taskcore/types"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
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
}

func NewWorker(globalCtx *globalctx.GlobalContext, cfg *config.Config, model model.ModelInterface, taskHandler TaskHandler) (WorkerInterface, error) {
	pollInterval := 1 * time.Second
	if cfg.Worker.PollInterval != nil {
		pollInterval = *cfg.Worker.PollInterval
	}
	w := &Worker{
		model:            model,
		lifeCycleHandler: NewTaskLifeCycleHandler(model, taskHandler),
		globalCtx:        globalCtx,
		taskHandler:      taskHandler,
		pollInterval:     pollInterval,
	}

	return w, nil
}

func (w *Worker) Start() {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.globalCtx.Context().Done():
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
	if err := w.model.RunTransactionWithTx(parentCtx, func(tx pgx.Tx, txm model.ModelInterface) error {
		qtask, err := txm.PullTask(parentCtx)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}

		metrics.PulledTasks.Inc()

		task := types.TaskToAPI(qtask)

		return w.runTaskWithTx(parentCtx, tx, task)

	}); err != nil {
		return err
	}
	return nil
}

func (w *Worker) RunTask(ctx context.Context, taskID int32) error {
	return w.model.RunTransactionWithTx(ctx, func(tx pgx.Tx, txm model.ModelInterface) error {
		qtask, err := txm.PullTaskByID(ctx, taskID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		task := types.TaskToAPI(qtask)
		return w.runTaskWithTx(ctx, tx, task)
	})
}

func (w *Worker) runTaskWithTx(_ctx context.Context, tx pgx.Tx, task apigen.Task) error {
	txm := w.model.SpawnWithTx(tx)

	// increment attempts
	if err := txm.IncrementAttempts(_ctx, task.ID); err != nil {
		return errors.Wrap(err, "failed to increment attempts")
	}
	task.Attempts++

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

	log.Info("executing task", zap.Int32("task_id", task.ID), zap.Any("task", task))

	// handle attributes
	if err := w.lifeCycleHandler.HandleAttributes(ctx, tx, task); err != nil {
		return errors.Wrap(err, "failed to handle attributes")
	}

	// run task
	err := w.taskHandler.HandleTask(ctx, tx, &task.Spec)
	if err != nil { // handle failed
		if err != taskcore.ErrRetryTaskWithoutErrorEvent {
			log.Error("error executing task", zap.Int32("task_id", task.ID), zap.Error(err))
		}
		if err := w.lifeCycleHandler.HandleFailed(ctx, tx, task, err); err != nil {
			return errors.Wrap(err, "failed to handle failed task")
		}
	} else { // handle completed
		if err := w.lifeCycleHandler.HandleCompleted(ctx, tx, task); err != nil {
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
