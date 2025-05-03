package worker

import (
	"context"

	"github.com/cloudcarver/anchor/internal/apigen"
	"github.com/cloudcarver/anchor/internal/model"
)

type TaskHandler interface {
	HandleTask(c *model.Context, spec *apigen.TaskSpec) error
}

type TaskLifeCycleHandlerInterface interface {
	HandleAttributes(ctx context.Context, task apigen.Task) error
	HandleFailed(ctx context.Context, task apigen.Task, err error) error
	HandleCompleted(ctx context.Context, task apigen.Task) error
}
