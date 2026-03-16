package wire

import (
	"errors"

	"github.com/cloudcarver/anclax/pkg/asynctask"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
)

func NewConfiguredWorker(globalCtx *globalctx.GlobalContext, cfg *config.Config, m model.ModelInterface, taskHandler worker.TaskHandler, executor *asynctask.Executor) (worker.WorkerInterface, error) {
	if executor == nil {
		return nil, errors.New("executor cannot be nil")
	}
	components, err := worker.BuildWorkerComponents(cfg, m, taskHandler)
	if err != nil {
		return nil, err
	}
	workerInstance, err := worker.NewWorker(globalCtx, components, taskHandler)
	if err != nil {
		return nil, err
	}
	workerInstance.RegisterTaskHandler(asynctask.NewWorkerControlTaskHandler(workerInstance))
	executor.SetLocalWorker(workerInstance)
	return workerInstance, nil
}
