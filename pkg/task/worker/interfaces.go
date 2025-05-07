package worker

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cloudcarver/anchor/pkg/apigen"
	"github.com/cloudcarver/anchor/pkg/model"
)

var ErrUnknownTaskType = errors.New("unknown task type")

type TaskSpec interface {
	GetType() string
	GetPayload() json.RawMessage
}

type TaskHandler interface {
	HandleTask(c *model.Context, spec TaskSpec) error
	RegisterTaskHandler(handler TaskHandler)
}

type TaskLifeCycleHandlerInterface interface {
	HandleAttributes(ctx context.Context, task apigen.Task) error
	HandleFailed(ctx context.Context, task apigen.Task, err error) error
	HandleCompleted(ctx context.Context, task apigen.Task) error
}
