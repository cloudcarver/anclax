package worker

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
	"github.com/cloudcarver/anclax/pkg/taskcore/pgnotify"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

var log = logger.NewLogAgent("worker")

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

	if runtimeConfigPoll < 0 {
		runtimeConfigPoll = 0
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
			log.Error("worker runtime error", zap.Error(err))
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
		ready := make(chan struct{})
		errCh := make(chan error, 1)
		go w.workerNotifyListenLoop(ctx, ready, errCh)
		if err := waitForListenReady(ctx, errCh, ready); err != nil {
			log.Error("worker listen setup failed", zap.Error(err))
			w.globalCtx.Cancel()
			return
		}
		go func() {
			select {
			case err := <-errCh:
				if err != nil && ctx.Err() == nil {
					log.Error("worker listen loop exited", zap.Error(err))
					w.globalCtx.Cancel()
				}
			case <-ctx.Done():
			}
		}()
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
		panic("worker releaseSlot called without acquire")
	}
}

func (w *Worker) workerNotifyListenLoop(ctx context.Context, ready chan<- struct{}, errCh chan<- error) {
	if err := w.listenWorkerNotifications(ctx, ready); err != nil && ctx.Err() == nil {
		errCh <- err
	}
}

func (w *Worker) listenWorkerNotifications(ctx context.Context, ready chan<- struct{}) error {
	conn, err := pgx.Connect(ctx, w.runtimeListenDSN)
	if err != nil {
		return fmt.Errorf("connect worker listener: %w", err)
	}
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", pgnotify.ChannelRuntimeConfig)); err != nil {
		return fmt.Errorf("listen runtime config channel: %w", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", pgnotify.ChannelTaskInterrupt)); err != nil {
		return fmt.Errorf("listen task interrupt channel: %w", err)
	}
	signalListenReady(ready)

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
			return fmt.Errorf("wait for worker notification: %w", err)
		}
		switch notification.Channel {
		case pgnotify.ChannelRuntimeConfig:
			if err := w.handleRuntimeConfigNotification(ctx, notification.Payload); err != nil {
				log.Error("failed to handle runtime config notification", zap.Error(err), zap.String("payload", notification.Payload))
			}
		case pgnotify.ChannelTaskInterrupt:
			if err := w.handleTaskInterruptNotification(ctx, notification.Payload); err != nil {
				log.Error("failed to handle task interrupt notification", zap.Error(err), zap.String("payload", notification.Payload))
			}
		default:
			log.Warn("received unknown worker notification channel", zap.String("channel", notification.Channel))
		}
	}
}

func (w *Worker) handleRuntimeConfigNotification(ctx context.Context, payload string) error {
	requestID, shouldRefresh, err := parseRuntimeConfigNotificationPayload(payload)
	if err != nil {
		return err
	}
	if !shouldRefresh {
		return nil
	}
	w.runtime.NotifyRuntimeConfig(ctx, requestID)
	return nil
}

func parseRuntimeConfigNotificationPayload(payload string) (requestID string, shouldRefresh bool, err error) {
	env, err := pgnotify.ParseEnvelope(payload)
	if err != nil {
		return "", false, fmt.Errorf("unmarshal runtime config notification: %w", err)
	}
	if !pgnotify.MatchesOp(env.Op, pgnotify.OpUpdateRuntimeConfig) {
		return "", false, nil
	}
	var params pgnotify.RuntimeConfigParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return "", false, fmt.Errorf("unmarshal runtime config params: %w", err)
	}
	return params.RequestID, true, nil
}

func (w *Worker) handleTaskInterruptNotification(ctx context.Context, payload string) error {
	requestID, taskIDs, shouldProcess, err := parseTaskInterruptNotificationPayload(payload)
	if err != nil {
		return err
	}
	if !shouldProcess {
		return nil
	}
	for _, taskID := range taskIDs {
		w.port.InterruptTask(taskID, taskcore.ErrTaskInterrupted)
	}
	if err := w.port.AckTaskInterruptApplied(ctx, requestID); err != nil {
		return err
	}
	return nil
}

func parseTaskInterruptNotificationPayload(payload string) (string, []int32, bool, error) {
	env, err := pgnotify.ParseEnvelope(payload)
	if err != nil {
		return "", nil, false, fmt.Errorf("unmarshal task interrupt notification: %w", err)
	}
	if !pgnotify.MatchesOp(env.Op, pgnotify.OpInterruptTask) {
		return "", nil, false, nil
	}
	var params pgnotify.TaskInterruptParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return "", nil, false, fmt.Errorf("unmarshal task interrupt params: %w", err)
	}
	return params.RequestID, params.TaskIDs, true, nil
}

func waitForListenReady(ctx context.Context, errCh <-chan error, readyChans ...<-chan struct{}) error {
	for _, ready := range readyChans {
		select {
		case <-ready:
		case err := <-errCh:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func signalListenReady(ready chan<- struct{}) {
	if ready == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	close(ready)
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
