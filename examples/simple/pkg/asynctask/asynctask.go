package asynctask

import (
	"context"

	"myexampleapp/pkg/zcore/model"
	counter "myexampleapp/pkg/zgen/schemas/counter"
	"myexampleapp/pkg/zgen/taskgen"

	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
)

type Executor struct {
	model model.ModelInterface
}

func NewExecutor(model model.ModelInterface) taskgen.ExecutorInterface {
	return &Executor{
		model: model,
	}
}

func (e *Executor) ExecuteIncrementCounter(ctx context.Context, _ worker.Task, params *counter.IncrementCounterParams) error {
	_ = params
	return e.model.IncrementCounter(ctx)
}

func (e *Executor) ExecuteAutoIncrementCounter(ctx context.Context, _ worker.Task, params *counter.IncrementCounterParams) error {
	_ = params
	return e.model.IncrementCounter(ctx)
}
