package workerv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

var log = logger.NewLogAgent("workerv2")

const runtimeConfigChannel = "anclax_worker_runtime_config"

type runtimeConfigNotification struct {
	Op     string `json:"op"`
	Params struct {
		RequestID string `json:"request_id"`
	} `json:"params"`
}

type Worker struct {
	globalCtx *globalctx.GlobalContext

	engine  *Engine
	runtime *Runtime
	port    *ModelPort

	taskHandler TaskHandler
	semaphore   chan struct{}

	runtimeListenDSN string
}

func NewWorker(globalCtx *globalctx.GlobalContext, cfg *config.Config, m model.ModelInterface, taskHandler TaskHandler) (WorkerInterface, error) {
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

	concurrency := 10
	if cfg.Worker.Concurrency != nil {
		concurrency = *cfg.Worker.Concurrency
	}
	if concurrency < 1 {
		concurrency = 1
	}

	workerID := uuid.New()
	if cfg.Worker.WorkerID != nil {
		parsed, err := uuid.Parse(*cfg.Worker.WorkerID)
		if err != nil {
			return nil, fmt.Errorf("invalid workerId: %w", err)
		}
		workerID = parsed
	}

	maxStrictPercentage := int32(100)
	if cfg.Worker.MaxStrictPercentage != nil {
		maxStrictPercentage = int32(*cfg.Worker.MaxStrictPercentage)
	}

	port, err := NewModelPort(m, workerID, cfg.Worker.Labels, taskHandler, lockTTL, lockRefreshInterval)
	if err != nil {
		return nil, err
	}

	engine := NewEngine(EngineConfig{
		WorkerID:            workerID.String(),
		Labels:              cfg.Worker.Labels,
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
			log.Error("workerv2 runtime error", zap.Error(err))
		},
	})

	return &Worker{
		globalCtx:        globalCtx,
		engine:           engine,
		runtime:          runtime,
		port:             port,
		taskHandler:      taskHandler,
		semaphore:        make(chan struct{}, concurrency),
		runtimeListenDSN: runtimeListenDSNFromConfig(cfg),
	}, nil
}

func (w *Worker) Start() {
	ctx := w.globalCtx.Context()
	if w.runtimeListenDSN != "" {
		go w.runtimeConfigListenLoop(ctx)
	}
	w.runtime.Start(ctx)
}

func (w *Worker) RunTask(ctx context.Context, taskID int32) error {
	if err := w.acquireSlot(ctx); err != nil {
		return err
	}
	defer w.releaseSlot()

	task, err := w.port.ClaimByID(ctx, taskID, ClaimRequest{})
	if err != nil {
		if err == ErrNoTask {
			return nil
		}
		return err
	}
	if task == nil {
		return nil
	}

	execErr := w.port.ExecuteTask(ctx, *task)
	if err := w.port.FinalizeTask(ctx, *task, execErr); err != nil {
		return err
	}
	return nil
}

func (w *Worker) RegisterTaskHandler(handler TaskHandler) {
	if w.taskHandler == nil {
		return
	}
	w.taskHandler.RegisterTaskHandler(handler)
}

func (w *Worker) acquireSlot(ctx context.Context) error {
	select {
	case w.semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) releaseSlot() {
	select {
	case <-w.semaphore:
	default:
		panic("workerv2 releaseSlot called without acquire")
	}
}

func (w *Worker) runtimeConfigListenLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := w.listenRuntimeConfigUpdates(ctx); err != nil && ctx.Err() == nil {
			log.Error("runtime config listen loop exited, retrying", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (w *Worker) listenRuntimeConfigUpdates(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, w.runtimeListenDSN)
	if err != nil {
		return fmt.Errorf("connect runtime config listener: %w", err)
	}
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", runtimeConfigChannel)); err != nil {
		return fmt.Errorf("listen runtime config channel: %w", err)
	}

	for {
		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		notification, err := conn.WaitForNotification(waitCtx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("wait for runtime config notification: %w", err)
		}
		requestID, shouldRefresh, err := parseRuntimeConfigNotificationPayload(notification.Payload)
		if err != nil {
			log.Error("failed to parse runtime config notification", zap.Error(err), zap.String("payload", notification.Payload))
			continue
		}
		if !shouldRefresh {
			continue
		}
		w.runtime.NotifyRuntimeConfig(ctx, requestID)
	}
}

func parseRuntimeConfigNotificationPayload(payload string) (requestID string, shouldRefresh bool, err error) {
	var notification runtimeConfigNotification
	if err := json.Unmarshal([]byte(payload), &notification); err != nil {
		return "", false, fmt.Errorf("unmarshal runtime config notification: %w", err)
	}
	if notification.Op != "" && notification.Op != "up_config" {
		return "", false, nil
	}
	return notification.Params.RequestID, true, nil
}

func runtimeListenDSNFromConfig(cfg *config.Config) string {
	if cfg.Pg.DSN != nil && *cfg.Pg.DSN != "" {
		return *cfg.Pg.DSN
	}
	if cfg.Pg.User == "" || cfg.Pg.Host == "" || cfg.Pg.Port == 0 || cfg.Pg.Db == "" {
		return ""
	}
	dsnURL := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.Pg.User, cfg.Pg.Password),
		Host:     fmt.Sprintf("%s:%d", cfg.Pg.Host, cfg.Pg.Port),
		Path:     cfg.Pg.Db,
		RawQuery: "sslmode=" + utils.IfElse(cfg.Pg.SSLMode == "", "require", cfg.Pg.SSLMode),
	}
	return dsnURL.String()
}
