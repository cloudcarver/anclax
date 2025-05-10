package asynctask

import (
	"context"

	"myexampleapp/internal/zcore/model"
	"myexampleapp/internal/zgen/taskgen"
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
