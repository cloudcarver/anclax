package asynctask

import (
	"context"

	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
)

type Executor struct {
	model model.ModelInterface
}

func NewExecutor(model model.ModelInterface) taskgen.ExecutorInterface {
	return &Executor{model: model}
}

func (e *Executor) ExecuteDeleteOpaqueKey(ctx context.Context, tx model.Tx, params *taskgen.DeleteOpaqueKeyParameters) error {
	return e.model.DeleteOpaqueKey(ctx, params.KeyID)
}

func (e *Executor) OnDeleteOpaqueKeyFailed(ctx context.Context, taskID int32, params *taskgen.DeleteOpaqueKeyParameters, tx model.Tx) error {
	return nil
}
