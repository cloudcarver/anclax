package asynctask

import (
	"context"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
)

func (e *Executor) ExecuteDeleteOpaqueKey(ctx context.Context, _ worker.Task, params *taskgen.DeleteOpaqueKeyParameters) error {
	return e.model.DeleteOpaqueKey(ctx, params.KeyID)
}

func (e *Executor) OnDeleteOpaqueKeyFailed(ctx context.Context, taskID int32, params *taskgen.DeleteOpaqueKeyParameters, tx core.Tx) error {
	return nil
}
