package worker

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/jackc/pgx/v5"
)

var ErrUnknownTaskType = errors.New("unknown task type")

type TaskSpec interface {
	GetType() string
	GetPayload() json.RawMessage
}

type TaskHandler interface {
	HandleTask(ctx context.Context, tx pgx.Tx, spec TaskSpec) error

	RegisterTaskHandler(handler TaskHandler)

	OnTaskFailed(ctx context.Context, tx pgx.Tx, failedTaskSpec TaskSpec, taskID int32) error
}

type TaskLifeCycleHandlerInterface interface {
	HandleAttributes(ctx context.Context, tx pgx.Tx, task apigen.Task) error
	HandleFailed(ctx context.Context, tx pgx.Tx, task apigen.Task, err error) error
	HandleCompleted(ctx context.Context, tx pgx.Tx, task apigen.Task) error
}

type WorkerInterface interface {
	RunTask(ctx context.Context, taskID int32) error

	Start()

	RegisterTaskHandler(handler TaskHandler)
}
