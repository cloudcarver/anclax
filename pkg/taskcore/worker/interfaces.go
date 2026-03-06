package worker

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
)

var ErrUnknownTaskType = errors.New("unknown task type")

type TaskSpec struct {
	Spec apigen.TaskSpec
}

func (t TaskSpec) GetType() string {
	return t.Spec.Type
}

func (t TaskSpec) GetPayload() json.RawMessage {
	return t.Spec.Payload
}

func NewTaskSpec(spec apigen.TaskSpec) TaskSpec {
	return TaskSpec{Spec: spec}
}

type TaskHandler interface {
	HandleTask(ctx context.Context, spec TaskSpec) error
	OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec TaskSpec, taskID int32) error
	RegisterTaskHandler(handler TaskHandler)
}

type WorkerInterface interface {
	Start()
	RunTask(ctx context.Context, taskID int32) error
	RegisterTaskHandler(handler TaskHandler)
	WorkerID() string
	NotifyRuntimeConfig(requestID string)
	InterruptTasks(taskIDs []int32, cause error)
}
