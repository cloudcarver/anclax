package runner

import (
	"context"

	"github.com/cloudcarver/anchor/pkg/model"
)

type Executor struct {
	model model.ModelInterface
}

func NewExecutor(model model.ModelInterface) ExecutorInterface {
	return &Executor{model: model}
}

func (e *Executor) ExecuteDeleteOpaqueKey(ctx context.Context, params *DeleteOpaqueKeyParameters) error {
	return e.model.DeleteOpaqueKey(ctx, params.KeyID)
}
