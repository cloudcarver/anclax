package worker

import (
	"context"
	"time"

	"github.com/cloudcarver/anchor/pkg/taskcore"
	"github.com/cloudcarver/anchor/pkg/zcore/model"
	"github.com/cloudcarver/anchor/pkg/zgen/apigen"
	"github.com/cloudcarver/anchor/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

type TaskLifeCycleHandler struct {
	model        model.ModelInterface
	eventEmitter EventEmitter
	now          func() time.Time
}

func NewTaskLifeCycleHandler(model model.ModelInterface, eventEmitter EventEmitter) *TaskLifeCycleHandler {
	return &TaskLifeCycleHandler{
		model:        model,
		eventEmitter: eventEmitter,
		now:          time.Now,
	}
}

func (a *TaskLifeCycleHandler) HandleAttributes(ctx context.Context, tx pgx.Tx, task apigen.Task) error {
	if a.isCronjob(task) {
		return a.handleCronjob(ctx, tx, task)
	}
	return nil
}

func (a *TaskLifeCycleHandler) isCronjob(task apigen.Task) bool {
	return task.Attributes.Cronjob != nil
}

func (a *TaskLifeCycleHandler) HandleFailed(ctx context.Context, tx pgx.Tx, task apigen.Task, err error) error {
	txm := a.model.SpawnWithTx(tx)
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

	// cronjob should be run again if the error is not fatal
	if a.isCronjob(task) && err != taskcore.ErrFatalTask {
		log.Info("cronjob failed, will be run again", zap.Int32("task_id", task.ID))
		return nil
	}

	if err != taskcore.ErrFatalTask && task.Attributes.RetryPolicy != nil {
		if task.Attributes.RetryPolicy.AlwaysRetryOnFailure {
			// retry the task by updating the started_at field
			interval, err := time.ParseDuration(task.Attributes.RetryPolicy.Interval)
			if err != nil {
				return errors.Wrapf(err, "failed to parse retry interval: %s", task.Attributes.RetryPolicy.Interval)
			}
			nextTime := a.now().Add(interval)
			log.Info("task failed, schedule next run", zap.Int32("task_id", task.ID), zap.Time("next_time", nextTime))
			if err := txm.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{
				ID:        task.ID,
				StartedAt: &nextTime,
			}); err != nil {
				return errors.Wrap(err, "update task started at")
			}
			return nil
		}
	}

	// emit task failed event if configured
	if triggerErr := a.eventEmitter.EmitTaskFailed(ctx, tx, task.Spec.Type, task.ID); triggerErr != nil {
		return errors.Wrap(triggerErr, "failed to emit task failed event")
	}

	// update task status to failed
	if err := txm.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     task.ID,
		Status: string(apigen.Failed),
	}); err != nil {
		return errors.Wrap(err, "update task status")
	}
	return nil
}

func (a *TaskLifeCycleHandler) HandleCompleted(ctx context.Context, tx pgx.Tx, task apigen.Task) error {
	txm := a.model.SpawnWithTx(tx)
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
		return nil
	}

	if err := txm.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     task.ID,
		Status: string(apigen.Completed),
	}); err != nil {
		return errors.Wrap(err, "update task status")
	}
	return nil
}

func (a *TaskLifeCycleHandler) handleCronjob(ctx context.Context, tx pgx.Tx, task apigen.Task) error {
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
	if err := txm.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{
		ID:        task.ID,
		StartedAt: &nextTime,
	}); err != nil {
		return errors.Wrap(err, "failed to create task")
	}
	return nil
}
