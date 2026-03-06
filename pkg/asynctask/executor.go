package asynctask

import (
	"context"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
)

const (
	defaultWorkerHeartbeatInterval      = 3 * time.Second
	runtimeConfigHeartbeatTTLMultiplier = 3
)

type Executor struct {
	model                     model.ModelInterface
	runner                    taskgen.TaskRunner
	localWorker               worker.WorkerInterface
	now                       func() time.Time
	waitForAck                func(ctx context.Context, requestID string, listenTimeout time.Duration) error
	runtimeConfigHeartbeatTTL time.Duration
	runtimeListenDSN          string
}

func NewExecutor(cfg *config.Config, model model.ModelInterface, runner taskgen.TaskRunner) *Executor {
	return &Executor{
		model:                     model,
		runner:                    runner,
		now:                       time.Now,
		waitForAck:                newRuntimeConfigAckWaiter(runtimeListenDSNFromConfig(cfg)),
		runtimeConfigHeartbeatTTL: runtimeConfigHeartbeatTTLFromConfig(cfg),
		runtimeListenDSN:          runtimeListenDSNFromConfig(cfg),
	}
}

func runtimeConfigHeartbeatTTLFromConfig(cfg *config.Config) time.Duration {
	heartbeatInterval := defaultWorkerHeartbeatInterval
	if cfg != nil && cfg.Worker.HeartbeatInterval != nil && *cfg.Worker.HeartbeatInterval > 0 {
		heartbeatInterval = *cfg.Worker.HeartbeatInterval
	}
	return heartbeatInterval * runtimeConfigHeartbeatTTLMultiplier
}

func (e *Executor) SetLocalWorker(w worker.WorkerInterface) {
	e.localWorker = w
}

func (e *Executor) localWorkerID() string {
	if e.localWorker == nil {
		return ""
	}
	return e.localWorker.WorkerID()
}
