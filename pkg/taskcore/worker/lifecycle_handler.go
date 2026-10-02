package worker

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/logger"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

var lifecycleLog = logger.NewLogAgent("worker.lifecycle")

type TaskLifeCycleHandlerInterface interface {
	FinalizeAttempt(context.Context, core.Tx, Task, error) error
}

type TaskLifeCycleHandler struct {
	model       model.ModelInterface
	taskHandler TaskHandler
	workerID    uuid.UUID
	now         func() time.Time
}

func NewTaskLifeCycleHandler(m model.ModelInterface, handler TaskHandler, workerID uuid.UUID) TaskLifeCycleHandlerInterface {
	return &TaskLifeCycleHandler{model: m, taskHandler: handler, workerID: workerID, now: time.Now}
}

func (h *TaskLifeCycleHandler) FinalizeAttempt(ctx context.Context, tx core.Tx, task Task, execErr error) error {
	outcome, err := decideAttempt(task, execErr, h.now())
	if err != nil {
		return err
	}
	txm := h.model.SpawnWithTx(tx)
	status, err := txm.FinalizeTaskAttempt(ctx, querier.FinalizeTaskAttemptParams{
		ID: task.ID, WorkerID: uuid.NullUUID{UUID: h.workerID, Valid: true}, LeaseVersion: task.LeaseVersion,
		Status: string(outcome.status), StartedAt: outcome.startedAt, Attempts: outcome.attempts,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return taskcore.ErrTaskLockLost
	}
	if err != nil {
		return err
	}
	// A pause/cancel committed after execution started wins over this result.
	if status == string(apigen.Paused) || status == string(apigen.Cancelled) {
		return nil
	}
	if outcome.errorEvent {
		if err := h.insertTaskErrorEvent(ctx, txm, task.ID, execErr); err != nil {
			return err
		}
	}
	if outcome.completedEvent {
		if err := h.insertTaskCompletedEvent(ctx, txm, task.ID); err != nil {
			return err
		}
	}
	if outcome.failureHook && h.taskHandler != nil {
		return h.runFailureHook(ctx, tx, task)
	}
	return nil
}

func (h *TaskLifeCycleHandler) runFailureHook(ctx context.Context, tx core.Tx, task Task) error {
	if _, err := tx.Exec(ctx, "SAVEPOINT anclax_failure_hook"); err != nil {
		return err
	}
	hookErr := func() (err error) {
		defer func() {
			if value := recover(); value != nil {
				err = fmt.Errorf("failure hook panic: %v\n%s", value, debug.Stack())
			}
		}()
		return h.taskHandler.OnTaskFailed(ctx, tx, TaskSpec{Spec: task.Spec}, task.ID)
	}()
	if hookErr != nil {
		if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT anclax_failure_hook"); err != nil {
			return err
		}
		if !errors.Is(hookErr, ErrUnknownTaskType) {
			lifecycleLog.Error("task onFailed handler error", zap.Error(hookErr))
		}
	}
	_, err := tx.Exec(ctx, "RELEASE SAVEPOINT anclax_failure_hook")
	return err
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
