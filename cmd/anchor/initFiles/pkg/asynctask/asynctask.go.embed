package asynctask

import (
	"context"

	"myexampleapp/pkg/zcore/model"
	"myexampleapp/pkg/zgen/taskgen"

	"github.com/jackc/pgx/v5"
)

type Executor struct {
	model model.ModelInterface
}

func NewExecutor(model model.ModelInterface) taskgen.ExecutorInterface {
	return &Executor{
		model: model,
	}
}

func (e *Executor) ExecuteIncrementCounter(ctx context.Context, tx pgx.Tx, params *taskgen.IncrementCounterParameters) error {
	return e.model.IncrementCounter(ctx)
}

func (e *Executor) ExecuteAutoIncrementCounter(ctx context.Context, tx pgx.Tx, params *taskgen.AutoIncrementCounterParameters) error {
	return e.model.IncrementCounter(ctx)
}
