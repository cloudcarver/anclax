package worker

import (
	"context"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/taskcore"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

type TaskLifeCycleHandler struct {
	model       model.ModelInterface
	taskHandler TaskHandler
	now         func() time.Time
	workerID    uuid.UUID
}

func NewTaskLifeCycleHandler(model model.ModelInterface, taskHandler TaskHandler, workerID uuid.UUID) *TaskLifeCycleHandler {
	return &TaskLifeCycleHandler{
		model:       model,
		taskHandler: taskHandler,
		now:         time.Now,
		workerID:    workerID,
	}
}

func (a *TaskLifeCycleHandler) HandleAttributes(ctx context.Context, tx core.Tx, task apigen.Task) error {
	if a.isCronjob(task) {
		return a.handleCronjob(ctx, tx, task)
	}
	return nil
}

func (a *TaskLifeCycleHandler) isCronjob(task apigen.Task) bool {
	return task.Attributes.Cronjob != nil
}

func (a *TaskLifeCycleHandler) HandleFailed(ctx context.Context, tx core.Tx, task apigen.Task, err error) error {
	txm := a.model.SpawnWithTx(tx)
	if err := a.ensureOwnership(ctx, txm, task.ID); err != nil {
		return err
	}
	// insert error event if the error is not intentional
	if err != taskcore.ErrRetryTaskWithoutErrorEvent {
		if _, err := txm.InsertEvent(ctx, apigen.EventSpec{
			Type: apigen.TaskError,
			TaskError: &apigen.EventTaskError{
				TaskID: task.ID,
				Error:  err.Error(),
			},
		}); err != nil {
			return errors.Wrap(err, "insert task error event")
		}
	}

	// cronjob should be run again no matter what (fatal or not)
	if a.isCronjob(task) {
		log.Info("cronjob failed, will be run again", zap.Int32("task_id", task.ID))
		if err := a.releaseLock(ctx, txm, task.ID); err != nil {
			return err
		}
		return nil
	}

	if err != taskcore.ErrFatalTask && task.Attributes.RetryPolicy != nil {
		if task.Attributes.RetryPolicy.MaxAttempts == -1 || task.Attempts < task.Attributes.RetryPolicy.MaxAttempts {
			// retry the task by updating the started_at field
			interval, err := time.ParseDuration(task.Attributes.RetryPolicy.Interval)
			if err != nil {
				return errors.Wrapf(err, "failed to parse retry interval: %s", task.Attributes.RetryPolicy.Interval)
			}
			nextTime := a.now().Add(interval)
			log.Info("task failed, schedule next run", zap.Int32("task_id", task.ID), zap.Time("next_time", nextTime))
			if _, err := txm.UpdateTaskStartedAtByWorker(ctx, querier.UpdateTaskStartedAtByWorkerParams{
				ID:        task.ID,
				StartedAt: &nextTime,
				WorkerID:  a.workerIDParam(),
			}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return taskcore.ErrTaskLockLost
				}
				return errors.Wrap(err, "update task started at")
			}
			if err := a.releaseLock(ctx, txm, task.ID); err != nil {
				return err
			}
			return nil
		}
	}

	// emit task failed hook if configured
	if triggerErr := a.taskHandler.OnTaskFailed(ctx, tx, &task.Spec, task.ID); triggerErr != nil {
		return errors.Wrap(triggerErr, "failed to emit task failed hook")
	}

	// update task status to failed
	if _, err := txm.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       task.ID,
		Status:   string(apigen.Failed),
		WorkerID: a.workerIDParam(),
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return taskcore.ErrTaskLockLost
		}
		return errors.Wrap(err, "update task status")
	}
	return nil
}

func (a *TaskLifeCycleHandler) HandleCompleted(ctx context.Context, tx core.Tx, task apigen.Task) error {
	txm := a.model.SpawnWithTx(tx)
	if err := a.ensureOwnership(ctx, txm, task.ID); err != nil {
		return err
	}
	// the event must be reported
	if _, err := txm.InsertEvent(ctx, apigen.EventSpec{
		Type: apigen.TaskCompleted,
		TaskCompleted: &apigen.EventTaskCompleted{
			TaskID: task.ID,
		},
	}); err != nil {
		return errors.Wrap(err, "insert task completed event")
	}

	// cronjob should be run again anyway, no need to update status
	if a.isCronjob(task) {
		log.Info("cronjob success, will be run again", zap.Int32("task_id", task.ID))
		if err := a.releaseLock(ctx, txm, task.ID); err != nil {
			return err
		}
		return nil
	}

	if _, err := txm.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       task.ID,
		Status:   string(apigen.Completed),
		WorkerID: a.workerIDParam(),
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return taskcore.ErrTaskLockLost
		}
		return errors.Wrap(err, "update task status")
	}
	return nil
}

func (a *TaskLifeCycleHandler) handleCronjob(ctx context.Context, tx core.Tx, task apigen.Task) error {
	if task.Attributes.Cronjob == nil {
		return errors.Errorf("")
	}
	cronjob := task.Attributes.Cronjob
	txm := a.model.SpawnWithTx(tx)

	// schedule next task
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cron, err := parser.Parse(cronjob.CronExpression)
	if err != nil {
		return errors.Wrapf(err, "failed to parse cron expression: %s", cronjob.CronExpression)
	}
	nextTime := cron.Next(a.now())
	if _, err := txm.UpdateTaskStartedAtByWorker(ctx, querier.UpdateTaskStartedAtByWorkerParams{
		ID:        task.ID,
		StartedAt: &nextTime,
		WorkerID:  a.workerIDParam(),
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return taskcore.ErrTaskLockLost
		}
		return errors.Wrap(err, "failed to update task started at")
	}
	return nil
}

func (a *TaskLifeCycleHandler) ensureOwnership(ctx context.Context, txm model.ModelInterface, taskID int32) error {
	if _, err := txm.VerifyTaskOwnership(ctx, querier.VerifyTaskOwnershipParams{
		ID:       taskID,
		WorkerID: a.workerIDParam(),
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return taskcore.ErrTaskLockLost
		}
		return errors.Wrap(err, "verify task ownership")
	}
	return nil
}

func (a *TaskLifeCycleHandler) releaseLock(ctx context.Context, txm model.ModelInterface, taskID int32) error {
	if _, err := txm.ReleaseTaskLockByWorker(ctx, querier.ReleaseTaskLockByWorkerParams{
		ID:       taskID,
		WorkerID: a.workerIDParam(),
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return taskcore.ErrTaskLockLost
		}
		return errors.Wrap(err, "release task lock")
	}
	return nil
}

func (a *TaskLifeCycleHandler) workerIDParam() uuid.NullUUID {
	return uuid.NullUUID{UUID: a.workerID, Valid: true}
}
