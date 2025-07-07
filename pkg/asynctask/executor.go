package asynctask

import (
	"context"

	"github.com/cloudcarver/anchor/pkg/zcore/model"
	"github.com/cloudcarver/anchor/pkg/zgen/taskgen"
	"github.com/jackc/pgx/v5"
)

type Executor struct {
	model model.ModelInterface
}

func NewExecutor(model model.ModelInterface) taskgen.ExecutorInterface {
	return &Executor{model: model}
}

func (e *Executor) ExecuteDeleteOpaqueKey(ctx context.Context, params *taskgen.DeleteOpaqueKeyParameters) error {
	return e.model.DeleteOpaqueKey(ctx, params.KeyID)
}

func (e *Executor) OnDeleteOpaqueKeyFailed(ctx context.Context, taskID int32, params *taskgen.DeleteOpaqueKeyParameters, tx pgx.Tx) error {
	return nil
}
