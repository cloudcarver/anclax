package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/logger"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

var lifecycleLog = logger.NewLogAgent("worker.lifecycle")

type TaskLifeCycleHandlerInterface interface {
	HandleAttributes(ctx context.Context, tx core.Tx, task apigen.Task) error
	HandleFailed(ctx context.Context, tx core.Tx, task apigen.Task, execErr error) error
	HandleCompleted(ctx context.Context, tx core.Tx, task apigen.Task) error
}

type TaskLifeCycleHandler struct {
	model       model.ModelInterface
	taskHandler TaskHandler
	workerID    uuid.UUID
	now         func() time.Time
}

func NewTaskLifeCycleHandler(model model.ModelInterface, taskHandler TaskHandler, workerID uuid.UUID) TaskLifeCycleHandlerInterface {
	return &TaskLifeCycleHandler{
		model:       model,
		taskHandler: taskHandler,
		workerID:    workerID,
		now:         time.Now,
	}
}

func (h *TaskLifeCycleHandler) HandleAttributes(ctx context.Context, tx core.Tx, task apigen.Task) error {
	return nil
}

func (h *TaskLifeCycleHandler) HandleFailed(ctx context.Context, tx core.Tx, task apigen.Task, execErr error) error {
	if execErr == nil {
		return nil
	}
	if errors.Is(execErr, taskcore.ErrTaskLockLost) {
		return taskcore.ErrTaskLockLost
	}

	txm := h.model.SpawnWithTx(tx)
	statusOverride := failureStatusOverride(execErr)
	if statusOverride != "" {
		if err := h.updateTaskStatusByWorker(ctx, txm, task.ID, statusOverride); err != nil {
			return err
		}
		return nil
	}

	skipErrorEvent := errors.Is(execErr, taskcore.ErrRetryTaskWithoutErrorEvent)
	retryPolicy := task.Attributes.RetryPolicy
	shouldRetry := shouldRetryTask(execErr, retryPolicy, task.Attempts)
	if shouldRetry {
		if retryPolicy == nil {
			return h.handlePermanentFailure(ctx, tx, txm, task, execErr, false)
		}
		nextTime, err := nextRetryTime(retryPolicy.Interval, h.now())
		if err != nil {
			return err
		}
		if err := h.updateTaskStartedAtByWorker(ctx, txm, task.ID, nextTime); err != nil {
			return err
		}
		if !skipErrorEvent {
			if err := h.insertTaskErrorEvent(ctx, txm, task.ID, execErr); err != nil {
				return err
			}
		}
		if err := h.releaseTaskLockByWorker(ctx, txm, task.ID); err != nil {
			return err
		}
		return nil
	}

	return h.handlePermanentFailure(ctx, tx, txm, task, execErr, false)
}

func (h *TaskLifeCycleHandler) HandleCompleted(ctx context.Context, tx core.Tx, task apigen.Task) error {
	txm := h.model.SpawnWithTx(tx)
	if task.Attributes.Cronjob != nil {
		nextTime, err := nextCronTime(task.Attributes.Cronjob.CronExpression, h.now())
		if err != nil {
			return err
		}
		if err := h.updateTaskStartedAtByWorker(ctx, txm, task.ID, nextTime); err != nil {
			return err
		}
		if err := h.releaseTaskLockByWorker(ctx, txm, task.ID); err != nil {
			return err
		}
		return nil
	}

	if err := h.updateTaskStatusByWorker(ctx, txm, task.ID, apigen.Completed); err != nil {
		return err
	}
	if err := h.insertTaskCompletedEvent(ctx, txm, task.ID); err != nil {
		return err
	}
	return nil
}

func failureStatusOverride(execErr error) apigen.TaskStatus {
	switch {
	case errors.Is(execErr, taskcore.ErrTaskCancelled):
		return apigen.Cancelled
	case errors.Is(execErr, taskcore.ErrTaskPaused):
		return apigen.Paused
	default:
		return ""
	}
}

func shouldRetryTask(execErr error, policy *apigen.TaskRetryPolicy, attempts int32) bool {
	if errors.Is(execErr, taskcore.ErrFatalTask) {
		return false
	}
	if policy == nil {
		return false
	}
	if policy.MaxAttempts < 0 {
		return true
	}
	return attempts < policy.MaxAttempts
}

func nextRetryTime(interval string, now time.Time) (time.Time, error) {
	if interval == "" {
		return time.Time{}, errors.New("retry policy interval is empty")
	}
	duration, err := time.ParseDuration(interval)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid retry policy interval: %w", err)
	}
	return now.Add(duration), nil
}

func nextCronTime(expr string, now time.Time) (time.Time, error) {
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cronExpr, err := parser.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression: %w", err)
	}
	return cronExpr.Next(now), nil
}

func (h *TaskLifeCycleHandler) handlePermanentFailure(ctx context.Context, tx core.Tx, txm model.ModelInterface, task apigen.Task, execErr error, skipErrorEvent bool) error {
	if !skipErrorEvent {
		if err := h.insertTaskErrorEvent(ctx, txm, task.ID, execErr); err != nil {
			return err
		}
	}
	if err := h.updateTaskStatusByWorker(ctx, txm, task.ID, apigen.Failed); err != nil {
		return err
	}
	if h.taskHandler != nil {
		if err := h.taskHandler.OnTaskFailed(ctx, tx, TaskSpec{Spec: task.Spec}, task.ID); err != nil {
			if !errors.Is(err, ErrUnknownTaskType) {
				lifecycleLog.Error("task onFailed handler error", zap.Error(err))
			}
		}
	}
	return nil
}

func (h *TaskLifeCycleHandler) updateTaskStatusByWorker(ctx context.Context, txm model.ModelInterface, taskID int32, status apigen.TaskStatus) error {
	if _, err := txm.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       taskID,
		Status:   string(status),
		WorkerID: uuid.NullUUID{UUID: h.workerID, Valid: true},
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return taskcore.ErrTaskLockLost
		}
		return err
	}
	return nil
}

func (h *TaskLifeCycleHandler) updateTaskStartedAtByWorker(ctx context.Context, txm model.ModelInterface, taskID int32, startedAt time.Time) error {
	if _, err := txm.UpdateTaskStartedAtByWorker(ctx, querier.UpdateTaskStartedAtByWorkerParams{
		ID:        taskID,
		StartedAt: &startedAt,
		WorkerID:  uuid.NullUUID{UUID: h.workerID, Valid: true},
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return taskcore.ErrTaskLockLost
		}
		return err
	}
	return nil
}

func (h *TaskLifeCycleHandler) releaseTaskLockByWorker(ctx context.Context, txm model.ModelInterface, taskID int32) error {
	if _, err := txm.ReleaseTaskLockByWorker(ctx, querier.ReleaseTaskLockByWorkerParams{
		ID:       taskID,
		WorkerID: uuid.NullUUID{UUID: h.workerID, Valid: true},
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return taskcore.ErrTaskLockLost
		}
		return err
	}
	return nil
}

func (h *TaskLifeCycleHandler) insertTaskErrorEvent(ctx context.Context, txm model.ModelInterface, taskID int32, execErr error) error {
	_, err := txm.InsertEvent(ctx, apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  execErr.Error(),
		},
	})
	if err != nil {
		return fmt.Errorf("insert task error event: %w", err)
	}
	return nil
}

func (h *TaskLifeCycleHandler) insertTaskCompletedEvent(ctx context.Context, txm model.ModelInterface, taskID int32) error {
	_, err := txm.InsertEvent(ctx, apigen.EventSpec{
		Type: apigen.TaskCompleted,
		TaskCompleted: &apigen.EventTaskCompleted{
			TaskID: taskID,
		},
	})
	if err != nil {
		return fmt.Errorf("insert task completed event: %w", err)
	}
	return nil
}
