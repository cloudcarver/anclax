package asynctask

import (
	"context"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
)

const (
	defaultWorkerHeartbeatInterval      = 3 * time.Second
	runtimeConfigHeartbeatTTLMultiplier = 3
)

type Executor struct {
	model                     model.ModelInterface
	now                       func() time.Time
	waitForAck                func(ctx context.Context, requestID string, listenTimeout time.Duration) error
	runtimeConfigHeartbeatTTL time.Duration
}

func NewExecutor(cfg *config.Config, model model.ModelInterface) taskgen.ExecutorInterface {
	return &Executor{
		model:                     model,
		now:                       time.Now,
		waitForAck:                newRuntimeConfigAckWaiter(runtimeListenDSNFromConfig(cfg)),
		runtimeConfigHeartbeatTTL: runtimeConfigHeartbeatTTLFromConfig(cfg),
	}
}

func runtimeConfigHeartbeatTTLFromConfig(cfg *config.Config) time.Duration {
	heartbeatInterval := defaultWorkerHeartbeatInterval
	if cfg != nil && cfg.Worker.HeartbeatInterval != nil && *cfg.Worker.HeartbeatInterval > 0 {
		heartbeatInterval = *cfg.Worker.HeartbeatInterval
	}
	return heartbeatInterval * runtimeConfigHeartbeatTTLMultiplier
}
