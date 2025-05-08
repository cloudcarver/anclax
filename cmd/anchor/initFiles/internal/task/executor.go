package task

import (
	"context"

	"github.com/cloudcarver/anchor/example-app/internal/model"
)

type Executor struct {
	model model.ModelInterface
}

func NewExecutor(model model.ModelInterface) ExecutorInterface {
	return &Executor{
		model: model,
	}
}

func (e *Executor) IncrementCounter(ctx context.Context, params *IncrementCounterParameters) error {
	return e.model.IncrementCounter(ctx)
}
