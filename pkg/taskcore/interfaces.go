package taskcore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/jackc/pgx/v5"
)

var (
	// The task is fatal and should not be retried
	ErrFatalTask = errors.New("fatal task")

	// The error of the executor is intentional, no need to insert error event
	ErrRetryTaskWithoutErrorEvent = errors.New("retry task without error event")
)

type TaskOverride = func(task *apigen.Task) error

type TaskStoreInterface interface {
	PushTask(ctx context.Context, task *apigen.Task) (int32, error)

	UpdateCronJob(ctx context.Context, taskID int32, cronExpression string, spec json.RawMessage) error

	PauseTask(ctx context.Context, taskID int32) error

	ResumeTask(ctx context.Context, taskID int32) error

	WithTx(tx pgx.Tx) TaskStoreInterface

	GetTaskByUniqueTag(ctx context.Context, uniqueTag string) (*apigen.Task, error)
}
