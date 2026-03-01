package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/cloudcarver/anclax/pkg/metrics"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/types"
	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

var log = logger.NewLogAgent("worker")

const (
	defaultWeightGroup     = "__default__"
	defaultWeightConfigKey = "default"
	runtimeConfigChannel   = "anclax_worker_runtime_config"
	runtimeConfigAckChan   = "anclax_worker_runtime_config_ack"
)

type runtimeConfigPayload struct {
	MaxStrictPercentage *int32           `json:"maxStrictPercentage,omitempty"`
	LabelWeights        map[string]int32 `json:"labelWeights,omitempty"`
}

type runtimeConfigNotification struct {
	Op     string `json:"op"`
	Params struct {
		Version   int64  `json:"version"`
		RequestID string `json:"request_id"`
	} `json:"params"`
}

type runtimeConfigAckNotification struct {
	Op     string `json:"op"`
	Params struct {
		RequestID      string `json:"request_id"`
		WorkerID       string `json:"worker_id"`
		AppliedVersion int64  `json:"applied_version"`
	} `json:"params"`
}

type Worker struct {
	model model.ModelInterface

	lifeCycleHandler TaskLifeCycleHandlerInterface

	globalCtx *globalctx.GlobalContext

	taskHandler TaskHandler

	pollInterval time.Duration

	workerID            uuid.UUID
	labels              []string
	hasLabels           bool
	labelsJSON          json.RawMessage
	heartbeatInterval   time.Duration
	runtimeListenDSN    string
	runtimeConfigPoll   time.Duration
	lockTTL             time.Duration
	lockRefreshInterval time.Duration
	concurrency         int
	semaphore           chan struct{}

	runtimeMu              sync.Mutex
	runtimeConfigVersion   int64
	maxStrictPercentage    int32
	strictCap              int
	strictInFlight         int
	weightedLabels         []string
	normalClaimWheel       []string
	normalClaimWheelCursor int

	now func() time.Time
}

func NewWorker(globalCtx *globalctx.GlobalContext, cfg *config.Config, model model.ModelInterface, taskHandler TaskHandler) (WorkerInterface, error) {
	pollInterval := 1 * time.Second
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
			return nil, errors.Wrap(err, "invalid workerId")
		}
		workerID = parsed
	}

	labels := cfg.Worker.Labels
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return nil, errors.Wrap(err, "marshal worker labels")
	}

	w := &Worker{
		model:               model,
		lifeCycleHandler:    NewTaskLifeCycleHandler(model, taskHandler, workerID),
		globalCtx:           globalCtx,
		taskHandler:         taskHandler,
		pollInterval:        pollInterval,
		workerID:            workerID,
		labels:              labels,
		hasLabels:           len(labels) > 0,
		labelsJSON:          labelsJSON,
		heartbeatInterval:   heartbeatInterval,
		runtimeListenDSN:    runtimeListenDSNFromConfig(cfg),
		runtimeConfigPoll:   runtimeConfigPoll,
		lockTTL:             lockTTL,
		lockRefreshInterval: lockRefreshInterval,
		concurrency:         concurrency,
		semaphore:           make(chan struct{}, concurrency),
		now:                 time.Now,
	}
	defaultStrictPercentage := int32(100)
	if cfg.Worker.MaxStrictPercentage != nil {
		defaultStrictPercentage = int32(*cfg.Worker.MaxStrictPercentage)
	}
	w.applyRuntimeConfigLocked(0, runtimeConfigPayload{
		MaxStrictPercentage: &defaultStrictPercentage,
		LabelWeights: map[string]int32{
			defaultWeightGroup: 1,
		},
	})

	return w, nil
}

func (w *Worker) Start() {
	if err := w.registerWorker(w.globalCtx.Context()); err != nil {
		log.Error("failed to register worker", zap.Error(err))
		return
	}
	go w.runtimeConfigLoop()
	go w.heartbeatLoop()

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.globalCtx.Context().Done():
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = w.markOffline(ctx)
			return
		case <-ticker.C:
			go func() {
				metrics.WorkerGoroutines.Inc()
				defer metrics.WorkerGoroutines.Dec()
				if err := w.pullAndRun(w.globalCtx.Context()); err != nil {
					metrics.RunTaskErrors.Inc()
					log.Error("error running task", zap.Error(err))
				}
			}()
		}
	}
}

func (w *Worker) tryAcquireSlot() bool {
	if w.semaphore == nil {
		return true
	}
	select {
	case w.semaphore <- struct{}{}:
		return true
	default:
		return false
	}
}

func (w *Worker) acquireSlot(ctx context.Context) error {
	if w.semaphore == nil {
		return nil
	}
	select {
	case w.semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) releaseSlot() {
	if w.semaphore == nil {
		return
	}
	select {
	case <-w.semaphore:
	default:
		panic("worker releaseSlot called without acquire")
	}
}

func (w *Worker) pullAndRun(parentCtx context.Context) error {
	if !w.tryAcquireSlot() {
		return nil
	}
	defer w.releaseSlot()
	task, err := w.claimTask(parentCtx)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}
	if w.isStrictTask(*task) {
		defer w.releaseStrictInFlight()
	}
	metrics.PulledTasks.Inc()
	return w.runTask(parentCtx, *task)
}

func (w *Worker) RunTask(ctx context.Context, taskID int32) error {
	if err := w.acquireSlot(ctx); err != nil {
		return err
	}
	defer w.releaseSlot()
	task, err := w.claimTaskByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}
	if w.isStrictTask(*task) {
		w.addStrictInFlight()
		defer w.releaseStrictInFlight()
	}
	return w.runTask(ctx, *task)
}

func (w *Worker) runTask(_ctx context.Context, task apigen.Task) error {
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)

	if task.Attributes.Timeout == nil {
		ctx, cancel = context.WithCancel(_ctx)
	} else {
		timeout, err := time.ParseDuration(*task.Attributes.Timeout)
		if err != nil {
			return errors.Wrap(err, "failed to parse timeout")
		}
		ctx, cancel = context.WithTimeout(_ctx, timeout)
	}
	defer cancel()

	refreshCancel := w.startLockRefresh(ctx, task.ID)
	defer refreshCancel()

	log.Info("executing task", zap.Int32("task_id", task.ID), zap.Any("task", task))

	if err := w.runHandleAttributes(ctx, task); err != nil {
		if errors.Is(err, taskcore.ErrTaskLockLost) {
			log.Warn("task lock lost before execution", zap.Int32("task_id", task.ID))
			return nil
		}
		return errors.Wrap(err, "failed to handle attributes")
	}

	err := w.taskHandler.HandleTask(ctx, &task.Spec)
	if err != nil { // handle failed
		if err != taskcore.ErrRetryTaskWithoutErrorEvent {
			log.Error("error executing task", zap.Int32("task_id", task.ID), zap.Error(err))
		}
		if err := w.runHandleFailed(ctx, task, err); err != nil {
			if errors.Is(err, taskcore.ErrTaskLockLost) {
				log.Warn("task lock lost after failure", zap.Int32("task_id", task.ID))
				return nil
			}
			return errors.Wrap(err, "failed to handle failed task")
		}
	} else { // handle completed
		if err := w.runHandleCompleted(ctx, task); err != nil {
			if errors.Is(err, taskcore.ErrTaskLockLost) {
				log.Warn("task lock lost after completion", zap.Int32("task_id", task.ID))
				return nil
			}
			log.Error("error handling completed task", zap.Int32("task_id", task.ID), zap.Error(err))
			return errors.Wrap(err, "failed to handle completed task")
		}
		log.Info("task completed", zap.Int32("task_id", task.ID))
	}
	return nil
}

func (w *Worker) RegisterTaskHandler(handler TaskHandler) {
	w.taskHandler.RegisterTaskHandler(handler)
}

func (w *Worker) claimTask(ctx context.Context) (*apigen.Task, error) {
	lockExpiry := w.now().Add(-w.lockTTL)
	workerID := w.workerIDParam()
	if w.tryReserveStrictInFlight() {
		task, err := w.claimStrictTask(ctx, workerID, lockExpiry)
		if err != nil {
			w.releaseStrictInFlight()
			return nil, err
		}
		if task != nil {
			return task, nil
		}
		w.releaseStrictInFlight()
	}

	groups, weightedLabels := w.nextNormalClaimGroups()
	for _, groupName := range groups {
		task, err := w.claimNormalTaskByGroup(ctx, workerID, lockExpiry, groupName, weightedLabels)
		if err != nil {
			return nil, err
		}
		if task != nil {
			return task, nil
		}
	}
	return nil, nil
}

func (w *Worker) claimStrictTask(ctx context.Context, workerID uuid.NullUUID, lockExpiry time.Time) (*apigen.Task, error) {
	var task *apigen.Task
	if err := w.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		qtask, err := txm.ClaimStrictTask(ctx, querier.ClaimStrictTaskParams{
			WorkerID:   workerID,
			LockExpiry: &lockExpiry,
			Labels:     w.labels,
			HasLabels:  w.hasLabels,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		converted := types.TaskToAPI(qtask)
		task = &converted
		return nil
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (w *Worker) claimNormalTaskByGroup(ctx context.Context, workerID uuid.NullUUID, lockExpiry time.Time, groupName string, weightedLabels []string) (*apigen.Task, error) {
	var task *apigen.Task
	if err := w.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		qtask, err := txm.ClaimNormalTaskByGroup(ctx, querier.ClaimNormalTaskByGroupParams{
			WorkerID:       workerID,
			LockExpiry:     &lockExpiry,
			Labels:         w.labels,
			HasLabels:      w.hasLabels,
			GroupName:      groupName,
			WeightedLabels: weightedLabels,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		converted := types.TaskToAPI(qtask)
		task = &converted
		return nil
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (w *Worker) claimTaskByID(ctx context.Context, taskID int32) (*apigen.Task, error) {
	var task *apigen.Task
	lockExpiry := w.now().Add(-w.lockTTL)
	workerID := w.workerIDParam()
	if err := w.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		qtask, err := txm.ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{
			ID:         taskID,
			WorkerID:   workerID,
			LockExpiry: &lockExpiry,
			Labels:     w.labels,
			HasLabels:  w.hasLabels,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		converted := types.TaskToAPI(qtask)
		task = &converted
		return nil
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (w *Worker) runHandleAttributes(ctx context.Context, task apigen.Task) error {
	return w.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		return w.lifeCycleHandler.HandleAttributes(ctx, tx, task)
	})
}

func (w *Worker) runHandleFailed(ctx context.Context, task apigen.Task, err error) error {
	return w.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		return w.lifeCycleHandler.HandleFailed(ctx, tx, task, err)
	})
}

func (w *Worker) runHandleCompleted(ctx context.Context, task apigen.Task) error {
	return w.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		return w.lifeCycleHandler.HandleCompleted(ctx, tx, task)
	})
}

func (w *Worker) startLockRefresh(ctx context.Context, taskID int32) context.CancelFunc {
	if w.lockRefreshInterval <= 0 {
		return func() {}
	}
	refreshCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(w.lockRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-ticker.C:
				if _, err := w.model.RefreshTaskLock(refreshCtx, querier.RefreshTaskLockParams{
					ID:       taskID,
					WorkerID: w.workerIDParam(),
				}); err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						log.Warn("task lock refresh failed, lock lost", zap.Int32("task_id", taskID))
						return
					}
					log.Error("failed to refresh task lock", zap.Int32("task_id", taskID), zap.Error(err))
				}
			}
		}
	}()
	return cancel
}

func (w *Worker) runtimeConfigLoop() {
	ctx := w.globalCtx.Context()
	if w.runtimeListenDSN == "" {
		log.Warn("runtime config listen DSN is empty, falling back to polling mode")
		if w.runtimeConfigPoll <= 0 {
			return
		}
		ticker := time.NewTicker(w.runtimeConfigPoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := w.refreshRuntimeConfig(ctx, ""); err != nil {
					log.Error("failed to refresh worker runtime config", zap.Error(err))
				}
			}
		}
	}

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

func (w *Worker) heartbeatLoop() {
	ticker := time.NewTicker(w.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.globalCtx.Context().Done():
			return
		case <-ticker.C:
			if err := w.heartbeat(w.globalCtx.Context()); err != nil {
				log.Error("failed to update worker heartbeat", zap.Error(err))
			}
		}
	}
}

func (w *Worker) listenRuntimeConfigUpdates(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, w.runtimeListenDSN)
	if err != nil {
		return errors.Wrap(err, "connect runtime config listener")
	}
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", runtimeConfigChannel)); err != nil {
		return errors.Wrap(err, "listen runtime config channel")
	}

	if err := w.refreshRuntimeConfig(ctx, ""); err != nil {
		log.Warn("failed to refresh runtime config after listen startup", zap.Error(err))
	}

	lastPoll := w.now()
	for {
		waitWindow := 5 * time.Second
		if w.runtimeConfigPoll > 0 {
			nextPoll := lastPoll.Add(w.runtimeConfigPoll)
			if untilPoll := time.Until(nextPoll); untilPoll < waitWindow {
				if untilPoll < 0 {
					waitWindow = 0
				} else {
					waitWindow = untilPoll
				}
			}
		}

		waitCtx, cancel := context.WithTimeout(ctx, waitWindow)
		notification, err := conn.WaitForNotification(waitCtx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				if w.runtimeConfigPoll > 0 && time.Since(lastPoll) >= w.runtimeConfigPoll {
					if err := w.refreshRuntimeConfig(ctx, ""); err != nil {
						log.Error("failed to refresh worker runtime config in fallback poll", zap.Error(err))
					}
					lastPoll = w.now()
				}
				continue
			}
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return nil
			}
			return errors.Wrap(err, "wait for runtime config notification")
		}
		if err := w.handleRuntimeConfigNotification(ctx, notification.Payload); err != nil {
			log.Error("failed to handle runtime config notification", zap.Error(err), zap.String("payload", notification.Payload))
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
	return w.refreshRuntimeConfig(ctx, requestID)
}

func (w *Worker) refreshRuntimeConfig(ctx context.Context, requestID string) error {
	cfg, err := w.model.GetLatestWorkerRuntimeConfig(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if requestID != "" {
				version := w.currentRuntimeConfigVersion()
				return w.notifyRuntimeConfigAck(ctx, requestID, version)
			}
			return nil
		}
		return err
	}
	currentVersion := w.currentRuntimeConfigVersion()
	if cfg.Version <= currentVersion {
		if requestID != "" {
			return w.notifyRuntimeConfigAck(ctx, requestID, currentVersion)
		}
		return nil
	}

	payload, err := decodeRuntimeConfigPayload(cfg.Payload)
	if err != nil {
		return err
	}

	w.runtimeMu.Lock()
	w.applyRuntimeConfigLocked(cfg.Version, payload)
	w.runtimeMu.Unlock()

	if err := w.model.UpdateWorkerAppliedConfigVersion(ctx, querier.UpdateWorkerAppliedConfigVersionParams{
		ID:                   w.workerID,
		AppliedConfigVersion: cfg.Version,
	}); err != nil {
		return errors.Wrap(err, "update worker applied config version")
	}
	if requestID != "" {
		return w.notifyRuntimeConfigAck(ctx, requestID, cfg.Version)
	}
	return nil
}

func (w *Worker) notifyRuntimeConfigAck(ctx context.Context, requestID string, appliedVersion int64) error {
	if requestID == "" {
		return nil
	}
	payload := runtimeConfigAckNotification{
		Op: "ack",
	}
	payload.Params.RequestID = requestID
	payload.Params.WorkerID = w.workerID.String()
	payload.Params.AppliedVersion = appliedVersion
	raw, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrap(err, "marshal runtime config ack notification")
	}
	if err := w.model.NotifyWorkerRuntimeConfigAck(ctx, string(raw)); err != nil {
		return errors.Wrap(err, "notify runtime config ack")
	}
	return nil
}

func (w *Worker) currentRuntimeConfigVersion() int64 {
	w.runtimeMu.Lock()
	defer w.runtimeMu.Unlock()
	return w.runtimeConfigVersion
}

func parseRuntimeConfigNotificationPayload(payload string) (requestID string, shouldRefresh bool, err error) {
	var notification runtimeConfigNotification
	if err := json.Unmarshal([]byte(payload), &notification); err != nil {
		return "", false, errors.Wrap(err, "unmarshal runtime config notification")
	}
	if notification.Op != "" && notification.Op != "up_config" {
		return "", false, nil
	}
	return notification.Params.RequestID, true, nil
}

func decodeRuntimeConfigPayload(raw json.RawMessage) (runtimeConfigPayload, error) {
	var payload runtimeConfigPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return runtimeConfigPayload{}, errors.Wrap(err, "unmarshal worker runtime config payload")
	}
	return payload, nil
}

func (w *Worker) applyRuntimeConfigLocked(version int64, payload runtimeConfigPayload) {
	percentage := int32(100)
	if payload.MaxStrictPercentage != nil {
		percentage = *payload.MaxStrictPercentage
	}
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 100 {
		percentage = 100
	}

	weights := make(map[string]int32, len(payload.LabelWeights)+1)
	for k, v := range payload.LabelWeights {
		if v < 1 {
			continue
		}
		if k == defaultWeightConfigKey {
			k = defaultWeightGroup
		}
		weights[k] = v
	}
	if _, ok := weights[defaultWeightGroup]; !ok {
		weights[defaultWeightGroup] = 1
	}

	weightedLabels := make([]string, 0, len(weights))
	for label := range weights {
		if label == defaultWeightGroup {
			continue
		}
		weightedLabels = append(weightedLabels, label)
	}
	sort.Strings(weightedLabels)

	wheel := buildClaimWheel(weights)
	if len(wheel) == 0 {
		wheel = []string{defaultWeightGroup}
	}
	w.runtimeConfigVersion = version
	w.maxStrictPercentage = percentage
	w.strictCap = strictCapForPercentage(w.concurrency, percentage)
	w.weightedLabels = weightedLabels
	w.normalClaimWheel = wheel
	if len(w.normalClaimWheel) > 0 {
		w.normalClaimWheelCursor = w.normalClaimWheelCursor % len(w.normalClaimWheel)
	} else {
		w.normalClaimWheelCursor = 0
	}
	metrics.WorkerStrictCap.Set(float64(w.strictCap))
	metrics.WorkerStrictInFlight.Set(float64(w.strictInFlight))
	metrics.WorkerRuntimeConfigVersion.Set(float64(w.runtimeConfigVersion))
}

func (w *Worker) tryReserveStrictInFlight() bool {
	w.runtimeMu.Lock()
	defer w.runtimeMu.Unlock()
	if w.strictInFlight >= w.strictCap {
		metrics.WorkerStrictSaturationTotal.Inc()
		return false
	}
	w.strictInFlight++
	metrics.WorkerStrictInFlight.Set(float64(w.strictInFlight))
	return true
}

func (w *Worker) addStrictInFlight() {
	w.runtimeMu.Lock()
	w.strictInFlight++
	metrics.WorkerStrictInFlight.Set(float64(w.strictInFlight))
	w.runtimeMu.Unlock()
}

func (w *Worker) releaseStrictInFlight() {
	w.runtimeMu.Lock()
	if w.strictInFlight > 0 {
		w.strictInFlight--
	}
	metrics.WorkerStrictInFlight.Set(float64(w.strictInFlight))
	w.runtimeMu.Unlock()
}

func (w *Worker) isStrictTask(task apigen.Task) bool {
	return task.Attributes.Priority != nil && *task.Attributes.Priority > 0
}

func (w *Worker) nextNormalClaimGroups() ([]string, []string) {
	w.runtimeMu.Lock()
	defer w.runtimeMu.Unlock()
	weightedLabels := append([]string(nil), w.weightedLabels...)
	if len(w.normalClaimWheel) == 0 {
		return []string{defaultWeightGroup}, weightedLabels
	}

	start := w.normalClaimWheelCursor
	w.normalClaimWheelCursor = (w.normalClaimWheelCursor + 1) % len(w.normalClaimWheel)

	order := make([]string, 0, len(w.normalClaimWheel))
	seen := make(map[string]struct{}, len(w.normalClaimWheel))
	for i := 0; i < len(w.normalClaimWheel); i++ {
		group := w.normalClaimWheel[(start+i)%len(w.normalClaimWheel)]
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		order = append(order, group)
	}
	return order, weightedLabels
}

func buildClaimWheel(weights map[string]int32) []string {
	groups := make([]string, 0, len(weights))
	for group := range weights {
		groups = append(groups, group)
	}
	sort.Strings(groups)

	wheel := make([]string, 0, len(groups))
	for _, group := range groups {
		weight := weights[group]
		for i := int32(0); i < weight; i++ {
			wheel = append(wheel, group)
		}
	}
	return wheel
}

func strictCapForPercentage(concurrency int, percentage int32) int {
	if concurrency <= 0 || percentage <= 0 {
		return 0
	}
	if percentage >= 100 {
		return concurrency
	}
	cap := (concurrency*int(percentage) + 99) / 100
	if cap < 1 {
		return 1
	}
	if cap > concurrency {
		return concurrency
	}
	return cap
}

func (w *Worker) registerWorker(ctx context.Context) error {
	version := w.currentRuntimeConfigVersion()
	_, err := w.model.UpsertWorker(ctx, querier.UpsertWorkerParams{
		ID:                   w.workerID,
		Labels:               w.labelsJSON,
		AppliedConfigVersion: version,
	})
	return err
}

func (w *Worker) heartbeat(ctx context.Context) error {
	_, err := w.model.UpdateWorkerHeartbeat(ctx, w.workerID)
	return err
}

func (w *Worker) markOffline(ctx context.Context) error {
	return w.model.MarkWorkerOffline(ctx, w.workerID)
}

func (w *Worker) workerIDParam() uuid.NullUUID {
	return uuid.NullUUID{UUID: w.workerID, Valid: true}
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
