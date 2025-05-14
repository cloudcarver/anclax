package asynctask

import (
	"context"

	"github.com/cloudcarver/anchor/pkg/hooks"
	"github.com/cloudcarver/anchor/pkg/zcore/model"
	"github.com/cloudcarver/anchor/pkg/zgen/taskgen"
)

type Executor struct {
	model model.ModelInterface
	hooks hooks.AnchorHookInterface
}

func NewExecutor(model model.ModelInterface) taskgen.ExecutorInterface {
	return &Executor{model: model}
}

func (e *Executor) ExecuteDeleteOpaqueKey(ctx context.Context, params *taskgen.DeleteOpaqueKeyParameters) error {
	return e.model.DeleteOpaqueKey(ctx, params.KeyID)
}
