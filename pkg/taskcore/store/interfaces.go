package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
)

var (
	// The task is fatal and should not be retried
	ErrFatalTask = errors.New("fatal task")

	// The error of the executor is intentional, no need to insert error event
	ErrRetryTaskWithoutErrorEvent = errors.New("retry task without error event")

	// The task lock is lost to another worker
	ErrTaskLockLost = errors.New("task lock lost")

	// The task execution was paused by control plane
	ErrTaskPaused = errors.New("task paused")

	// The task execution was cancelled by control plane
	ErrTaskCancelled = errors.New("task cancelled")

	// The task execution was interrupted by control plane
	ErrTaskInterrupted = errors.New("task interrupted")
)

type TaskOverride = func(task *apigen.Task) error

type TaskStoreInterface interface {
	PushTask(ctx context.Context, task *apigen.Task) (int32, error)

	UpdateCronJob(ctx context.Context, taskID int32, cronExpression string, spec json.RawMessage) error

	PauseTask(ctx context.Context, taskID int32) error

	CancelTask(ctx context.Context, taskID int32) error

	ResumeTask(ctx context.Context, taskID int32) error

	UpdatePendingTaskPriorityByLabels(ctx context.Context, labels []string, priority int32) (int64, error)

	UpdatePendingTaskWeightByLabels(ctx context.Context, labels []string, weight int32) (int64, error)

	WithTx(tx core.Tx) TaskStoreInterface

	GetTaskByUniqueTag(ctx context.Context, uniqueTag string) (*apigen.Task, error)

	GetTaskByID(ctx context.Context, taskID int32) (*apigen.Task, error)

	GetLastTaskErrorEvent(ctx context.Context, taskID int32) (*apigen.Event, error)

	WaitForTask(ctx context.Context, taskID int32, opts ...WaitForTaskOption) error
}
