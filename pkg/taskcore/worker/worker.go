package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var log = logger.NewLogAgent("worker")

type Worker struct {
	globalCtx *globalctx.GlobalContext

	engine  *Engine
	runtime *Runtime
	port    *ModelPort

	taskHandler TaskHandler
}

type WorkerComponents struct {
	Engine      *Engine
	Runtime     *Runtime
	Port        *ModelPort
	Concurrency int
}

func BuildWorkerComponents(cfg *config.Config, m model.ModelInterface, taskHandler TaskHandler) (*WorkerComponents, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	pollInterval := time.Second
	if cfg.Worker.PollInterval != nil {
		pollInterval = *cfg.Worker.PollInterval
	}

	heartbeatInterval := 3 * time.Second
	if cfg.Worker.HeartbeatInterval != nil {
		heartbeatInterval = *cfg.Worker.HeartbeatInterval
	}

	lockTTL := 9 * time.Second
	if cfg.Worker.LockTTL != nil {
		lockTTL = *cfg.Worker.LockTTL
	}

	lockRefreshInterval := heartbeatInterval
	if cfg.Worker.LockRefreshInterval != nil {
		lockRefreshInterval = *cfg.Worker.LockRefreshInterval
	}

	runtimeConfigPoll := time.Duration(0)
	if cfg.Worker.RuntimeConfigPollInterval != nil {
		runtimeConfigPoll = *cfg.Worker.RuntimeConfigPollInterval
	}
	if runtimeConfigPoll < 0 {
		runtimeConfigPoll = 0
	}

	concurrency := 10
	if cfg.Worker.Concurrency != nil {
		concurrency = *cfg.Worker.Concurrency
	}
	if concurrency < 1 {
		concurrency = 1
	}
	batchSize := DefaultClaimBatchSize
	if cfg.Worker.ClaimBatchSize != nil {
		batchSize = *cfg.Worker.ClaimBatchSize
	}
	if batchSize < 1 || batchSize > 256 {
		return nil, fmt.Errorf("worker.claimBatchSize must be between 1 and 256")
	}

	workerID := uuid.New()
	if cfg.Worker.WorkerID != nil {
		parsed, err := uuid.Parse(*cfg.Worker.WorkerID)
		if err != nil {
			return nil, fmt.Errorf("invalid workerId: %w", err)
		}
		workerID = parsed
	}

	labels := configuredWorkerLabels(cfg.Worker.Labels, workerID.String())

	maxStrictPercentage := int32(100)
	if cfg.Worker.MaxStrictPercentage != nil {
		maxStrictPercentage = int32(*cfg.Worker.MaxStrictPercentage)
	}

	port, err := NewModelPort(m, workerID, labels, taskHandler, lockTTL, lockRefreshInterval)
	if err != nil {
		return nil, err
	}

	engine := NewEngine(EngineConfig{
		ClaimBatchSize:      batchSize,
		ControlConcurrency:  1,
		WorkerID:            workerID.String(),
		Labels:              labels,
		Concurrency:         concurrency,
		MaxStrictPercentage: maxStrictPercentage,
		LabelWeights: map[string]int32{
			DefaultWeightGroup: 1,
		},
	})

	runtime := NewRuntime(engine, port, RuntimeOptions{
		PollInterval:          pollInterval,
		HeartbeatInterval:     heartbeatInterval,
		RuntimeConfigInterval: runtimeConfigPoll,
		OnError: func(err error) {
			log.Error("worker runtime error", zap.Error(err))
		},
	})

	return &WorkerComponents{
		Engine:      engine,
		Runtime:     runtime,
		Port:        port,
		Concurrency: concurrency,
	}, nil
}

func configuredWorkerLabels(labels []string, workerID string) []string {
	out := append([]string(nil), labels...)
	reserved := fmt.Sprintf("worker:%s", workerID)
	for _, label := range out {
		if label == reserved {
			return out
		}
	}
	out = append(out, reserved)
	return out
}

func NewWorker(globalCtx *globalctx.GlobalContext, components *WorkerComponents, taskHandler TaskHandler) (WorkerInterface, error) {
	if components == nil || components.Engine == nil || components.Runtime == nil || components.Port == nil {
		return nil, fmt.Errorf("worker components are incomplete")
	}
	return &Worker{
		globalCtx:   globalCtx,
		engine:      components.Engine,
		runtime:     components.Runtime,
		port:        components.Port,
		taskHandler: taskHandler,
	}, nil
}

func NewWorkerFromConfig(globalCtx *globalctx.GlobalContext, cfg *config.Config, m model.ModelInterface, taskHandler TaskHandler) (WorkerInterface, error) {
	components, err := BuildWorkerComponents(cfg, m, taskHandler)
	if err != nil {
		return nil, err
	}
	return NewWorker(globalCtx, components, taskHandler)
}

func (w *Worker) WorkerID() string {
	if w.engine == nil {
		return ""
	}
	return w.engine.WorkerID()
}

func (w *Worker) NotifyRuntimeConfig(requestID string) {
	if w.runtime == nil {
		return
	}
	w.runtime.NotifyRuntimeConfig(w.globalCtx.Context(), requestID)
}

func (w *Worker) InterruptTasks(taskIDs []int32, cause error) {
	if w.port == nil {
		return
	}
	for _, taskID := range taskIDs {
		w.port.InterruptTask(taskID, cause)
	}
}

func (w *Worker) WaitTaskRuntimes(ctx context.Context, taskIDs []int32) error {
	if w.port == nil {
		return nil
	}
	return w.port.WaitTaskRuntimes(ctx, taskIDs)
}

func (w *Worker) Start() {
	ctx := w.globalCtx.Context()
	w.runtime.Start(ctx)
	if ctx.Err() == nil {
		log.Error("worker runtime exited unexpectedly; shutting down worker")
		w.globalCtx.Cancel()
	}
}

func (w *Worker) RunTask(ctx context.Context, taskID int32) error {
	return w.runtime.RunTask(ctx, taskID)
}

func (w *Worker) TaskRuntimesActive(taskIDs []int32) bool {
	return w.port != nil && w.port.TaskRuntimesActive(taskIDs)
}

func (w *Worker) RegisterTaskHandler(handler TaskHandler) {
	if w.taskHandler == nil {
		return
	}
	w.taskHandler.RegisterTaskHandler(handler)
}
