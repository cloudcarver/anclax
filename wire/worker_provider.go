package wire

import (
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/taskcore/workerv2"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
)

func NewConfiguredWorker(globalCtx *globalctx.GlobalContext, cfg *config.Config, m model.ModelInterface, taskHandler worker.TaskHandler) (worker.WorkerInterface, error) {
	if cfg.Worker.UseLegacyWorker {
		return worker.NewWorker(globalCtx, cfg, m, taskHandler)
	}

	w, err := workerv2.NewWorker(globalCtx, cfg, m, taskHandler)
	if err != nil {
		return nil, err
	}
	return w, nil
}
