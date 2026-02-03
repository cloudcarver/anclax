package asynctask

import (
	"context"

	"myexampleapp/pkg/zcore/model"
	"myexampleapp/pkg/zgen/taskgen"
)

type Executor struct {
	model model.ModelInterface
}

func NewExecutor(model model.ModelInterface) taskgen.ExecutorInterface {
	return &Executor{
		model: model,
	}
}

func (e *Executor) ExecuteIncrementCounter(ctx context.Context, params *taskgen.IncrementCounterParameters) error {
	return e.model.IncrementCounter(ctx)
}

func (e *Executor) ExecuteAutoIncrementCounter(ctx context.Context, params *taskgen.AutoIncrementCounterParameters) error {
	return e.model.IncrementCounter(ctx)
}
