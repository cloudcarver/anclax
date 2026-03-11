//go:build smoke
// +build smoke

package taskcoree2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/asynctask"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/taskcore/ctrl"
	"github.com/cloudcarver/anclax/pkg/taskcore/pgnotify"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	smokeContainerName = "anclax-pg-smoke"
	smokePort          = "5499"
)

// This file centralizes smoke-test support:
// - Docker Postgres lifecycle
// - runtime actor used by DST scenarios
// - worker handlers and runtime-config payload helpers

func withSmokePostgres(t *testing.T, fn func(ctx context.Context, m model.ModelInterface)) {
	t.Helper()
	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	cleanupContainer(t)
	if err := runDocker(t, "run", "-d", "--name", smokeContainerName,
		"-e", "POSTGRES_PASSWORD=postgres",
		"-e", "POSTGRES_USER=postgres",
		"-e", "POSTGRES_DB=postgres",
		"-p", smokePort+":5432",
		"postgres:15",
	); err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}
	t.Cleanup(func() { cleanupContainer(t) })

	dsn := smokePostgresDSN()
	if err := waitForPostgres(t, dsn, 15*time.Second); err != nil {
		t.Fatalf("postgres not ready: %v", err)
	}

	cfg := &config.Config{Pg: config.Pg{DSN: &dsn}}
	libCfg := config.DefaultLibConfig()
	cm := closer.NewCloserManager()
	t.Cleanup(cm.Close)

	m, err := model.NewModel(cfg, libCfg, cm)
	if err != nil {
		t.Fatalf("failed to create model: %v", err)
	}

	fn(context.Background(), m)
}

func smokePostgresDSN() string {
	return "postgres://postgres:postgres@localhost:" + smokePort + "/postgres?sslmode=disable"
}

func dockerAvailable() bool {
	cmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	return cmd.Run() == nil
}

func runDocker(t *testing.T, args ...string) error {
	t.Helper()
	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %v failed: %w: %s", args, err, string(output))
	}
	return nil
}

func cleanupContainer(t *testing.T) {
	t.Helper()
	cmd := exec.Command("docker", "rm", "-f", smokeContainerName)
	_ = cmd.Run()
}

func waitForPostgres(t *testing.T, dsn string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			_ = conn.Close(ctx)
			cancel()
			return nil
		}
		cancel()
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for postgres")
}

func signalOnce(ch chan struct{}) {
	select {
	case <-ch:
		return
	default:
		close(ch)
	}
}

type smokeWorkerHandler struct {
	startedCh chan struct{}
	proceedCh chan struct{}
	doneCh    chan struct{}
}

func newSmokeWorkerHandler() *smokeWorkerHandler {
	return &smokeWorkerHandler{
		startedCh: make(chan struct{}),
		proceedCh: make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

func (h *smokeWorkerHandler) HandleTask(ctx context.Context, spec worker.Task) error {
	if spec.GetType() != "smoke-worker" {
		return worker.ErrUnknownTaskType
	}
	signalOnce(h.startedCh)
	select {
	case <-h.proceedCh:
	case <-ctx.Done():
		return ctx.Err()
	}
	signalOnce(h.doneCh)
	return nil
}

func (h *smokeWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *smokeWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

func (h *smokeWorkerHandler) release() {
	signalOnce(h.proceedCh)
}

func (h *smokeWorkerHandler) started() <-chan struct{} {
	return h.startedCh
}

func (h *smokeWorkerHandler) done() <-chan struct{} {
	return h.doneCh
}

type retryWorkerHandler struct {
	attempts        int32
	firstAttemptCh  chan struct{}
	secondAttemptCh chan struct{}
}

func newRetryWorkerHandler() *retryWorkerHandler {
	return &retryWorkerHandler{
		firstAttemptCh:  make(chan struct{}),
		secondAttemptCh: make(chan struct{}),
	}
}

func (h *retryWorkerHandler) HandleTask(ctx context.Context, spec worker.Task) error {
	if spec.GetType() != "smoke-retry" {
		return worker.ErrUnknownTaskType
	}
	attempt := atomic.AddInt32(&h.attempts, 1)
	if attempt == 1 {
		signalOnce(h.firstAttemptCh)
		return errors.New("retry attempt")
	}
	signalOnce(h.secondAttemptCh)
	return nil
}

func (h *retryWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *retryWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

func (h *retryWorkerHandler) firstAttempt() <-chan struct{} {
	return h.firstAttemptCh
}

func (h *retryWorkerHandler) secondAttempt() <-chan struct{} {
	return h.secondAttemptCh
}

type cronWorkerHandler struct {
	ranCh chan struct{}
}

func newCronWorkerHandler() *cronWorkerHandler {
	return &cronWorkerHandler{ranCh: make(chan struct{})}
}

func (h *cronWorkerHandler) HandleTask(ctx context.Context, spec worker.Task) error {
	if spec.GetType() != "smoke-cron" {
		return worker.ErrUnknownTaskType
	}
	signalOnce(h.ranCh)
	return nil
}

func (h *cronWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *cronWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

func (h *cronWorkerHandler) ran() <-chan struct{} {
	return h.ranCh
}

type noopWorkerHandler struct{}

func (h *noopWorkerHandler) HandleTask(ctx context.Context, spec worker.Task) error {
	return worker.ErrUnknownTaskType
}

func (h *noopWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *noopWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

type blockingWorkerHandler struct {
	taskType  string
	startedCh chan struct{}
	releaseCh chan struct{}
	doneCh    chan struct{}
}

func newBlockingWorkerHandler(taskType string) *blockingWorkerHandler {
	return &blockingWorkerHandler{
		taskType:  taskType,
		startedCh: make(chan struct{}),
		releaseCh: make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

func (h *blockingWorkerHandler) HandleTask(ctx context.Context, spec worker.Task) error {
	if spec.GetType() != h.taskType {
		return worker.ErrUnknownTaskType
	}
	signalOnce(h.startedCh)
	select {
	case <-h.releaseCh:
		signalOnce(h.doneCh)
		return nil
	case <-ctx.Done():
		signalOnce(h.doneCh)
		return ctx.Err()
	}
}

func (h *blockingWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *blockingWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

func (h *blockingWorkerHandler) release() {
	signalOnce(h.releaseCh)
}

func (h *blockingWorkerHandler) started() <-chan struct{} {
	return h.startedCh
}

func (h *blockingWorkerHandler) done() <-chan struct{} {
	return h.doneCh
}

type signalWorkerHandler struct {
	taskType string
	doneCh   chan struct{}
}

func newSignalWorkerHandler(taskType string) *signalWorkerHandler {
	return &signalWorkerHandler{taskType: taskType, doneCh: make(chan struct{})}
}

func (h *signalWorkerHandler) HandleTask(ctx context.Context, spec worker.Task) error {
	if spec.GetType() != h.taskType {
		return worker.ErrUnknownTaskType
	}
	signalOnce(h.doneCh)
	return nil
}

func (h *signalWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *signalWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

func (h *signalWorkerHandler) done() <-chan struct{} {
	return h.doneCh
}

type failureWorkerHandler struct {
	taskType string
	failErr  error
	failedCh chan struct{}
}

func newFailureWorkerHandler(taskType string, failErr error) *failureWorkerHandler {
	return &failureWorkerHandler{taskType: taskType, failErr: failErr, failedCh: make(chan struct{})}
}

func (h *failureWorkerHandler) HandleTask(ctx context.Context, spec worker.Task) error {
	if spec.GetType() != h.taskType {
		return worker.ErrUnknownTaskType
	}
	return h.failErr
}

func (h *failureWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *failureWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	signalOnce(h.failedCh)
	return nil
}

func (h *failureWorkerHandler) failed() <-chan struct{} {
	return h.failedCh
}

type runtimeConfigPayloadSpec struct {
	MaxStrictPercentage *int32           `json:"maxStrictPercentage,omitempty"`
	LabelWeights        map[string]int32 `json:"labelWeights,omitempty"`
}

type runtimeConfigNotifySpec struct {
	Op     string                    `json:"op"`
	Params runtimeConfigNotifyParams `json:"params"`
}

type runtimeConfigNotifyParams struct {
	RequestID string `json:"request_id"`
	Version   int64  `json:"version"`
}

type runtimeConfigAckSpec struct {
	Op     string                 `json:"op"`
	Params runtimeConfigAckParams `json:"params"`
}

type runtimeConfigAckParams struct {
	RequestID      string `json:"request_id"`
	WorkerID       string `json:"worker_id"`
	AppliedVersion int64  `json:"applied_version"`
}

type runtimeWorker struct {
	workerID uuid.UUID
	gctx     *globalctx.GlobalContext
	handler  any
}

type runtimeActor struct {
	model model.ModelInterface

	mu            sync.Mutex
	workers       map[string]*runtimeWorker
	lockAnchors   map[string]time.Time
	configVersion map[string]int64
	captures      *runtimeCaptureTracker
	contention    *runtimeContentionTracker
}

type controlPlaneActor struct {
	controlPlane *ctrl.WorkerControlPlane
	model        model.ModelInterface
}

func newControlPlaneActor(model model.ModelInterface, store taskcore.TaskStoreInterface) *controlPlaneActor {
	runner := taskgen.NewTaskRunner(store)
	controlPlane := ctrl.NewWorkerControlPlane(model, runner, store)
	return &controlPlaneActor{controlPlane: controlPlane, model: model}
}

func (a *controlPlaneActor) UpdateRuntimeConfig(ctx context.Context, key string, maxStrictPercentage int32, defaultWeight int32, w1Weight int32, w2Weight int32) error {
	if key == "" {
		return fmt.Errorf("runtime config key is required")
	}
	labels := make([]string, 0, 2)
	weights := make([]int32, 0, 2)
	if w1Weight > 0 {
		labels = append(labels, "w1")
		weights = append(weights, w1Weight)
	}
	if w2Weight > 0 {
		labels = append(labels, "w2")
		weights = append(weights, w2Weight)
	}
	req := &ctrl.UpdateWorkerRuntimeConfigRequest{
		MaxStrictPercentage: int32Ptr(maxStrictPercentage),
		DefaultWeight:       int32Ptr(defaultWeight),
		Labels:              labels,
		Weights:             weights,
	}
	if err := a.controlPlane.UpdateWorkerRuntimeConfig(ctx, req); err != nil {
		return err
	}
	return nil
}

func (a *controlPlaneActor) PauseTask(ctx context.Context, task string) error {
	if task == "" {
		return fmt.Errorf("task name is required")
	}
	taskID, err := a.taskIDByName(ctx, task)
	if err != nil {
		return err
	}
	return a.controlPlane.PauseTask(ctx, taskID)
}

func (a *controlPlaneActor) CancelTask(ctx context.Context, task string) error {
	if task == "" {
		return fmt.Errorf("task name is required")
	}
	taskID, err := a.taskIDByName(ctx, task)
	if err != nil {
		return err
	}
	return a.controlPlane.CancelTask(ctx, taskID)
}

func (a *controlPlaneActor) taskIDByName(ctx context.Context, task string) (int32, error) {
	var id int32
	err := a.model.RunTransactionWithTx(ctx, func(tx core.Tx, _ model.ModelInterface) error {
		return tx.QueryRow(ctx, "select id from anclax.tasks where spec->'payload'->>'name' = $1 order by created_at desc limit 1", task).Scan(&id)
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func newRuntimeActor(m model.ModelInterface) *runtimeActor {
	return &runtimeActor{
		model:         m,
		workers:       map[string]*runtimeWorker{},
		lockAnchors:   map[string]time.Time{},
		configVersion: map[string]int64{},
		captures:      newRuntimeCaptureTracker(),
		contention:    newRuntimeContentionTracker(),
	}
}

func (a *runtimeActor) StartWorker(
	ctx context.Context,
	name string,
	mode string,
	taskType string,
	labels []string,
	pollMs int32,
	lockRefreshMs int32,
	lockTTLms int32,
	heartbeatMs int32,
	concurrency int32,
	runtimePollMs int32,
	useDSN bool,
	workerIDRaw string,
) error {
	if name == "" {
		return fmt.Errorf("worker name is required")
	}

	workerID := uuid.New()
	if workerIDRaw != "" {
		parsed, err := uuid.Parse(workerIDRaw)
		if err != nil {
			return err
		}
		workerID = parsed
	}
	workerIDString := workerID.String()

	cfg := &config.Config{
		Worker: config.Worker{
			Labels:   append([]string(nil), labels...),
			WorkerID: &workerIDString,
		},
	}
	if useDSN {
		dsn := smokePostgresDSN()
		cfg.Pg = config.Pg{DSN: &dsn}
	}
	if pollMs > 0 {
		v := time.Duration(pollMs) * time.Millisecond
		cfg.Worker.PollInterval = &v
	}
	if lockTTLms > 0 {
		v := time.Duration(lockTTLms) * time.Millisecond
		cfg.Worker.LockTTL = &v
	}
	if lockRefreshMs >= 0 {
		v := time.Duration(lockRefreshMs) * time.Millisecond
		cfg.Worker.LockRefreshInterval = &v
	}
	if heartbeatMs > 0 {
		v := time.Duration(heartbeatMs) * time.Millisecond
		cfg.Worker.HeartbeatInterval = &v
	}
	if concurrency > 0 {
		c := int(concurrency)
		cfg.Worker.Concurrency = &c
	}
	if runtimePollMs > 0 {
		v := time.Duration(runtimePollMs) * time.Millisecond
		cfg.Worker.RuntimeConfigPollInterval = &v
	}

	baseHandler, err := a.newHandler(name, mode, taskType)
	if err != nil {
		return err
	}
	executor := asynctask.NewExecutor(cfg, a.model, taskgen.NewTaskRunner(taskcore.NewTaskStore(a.model)))
	compositeHandler := taskgen.NewTaskHandler(executor)
	compositeHandler.RegisterTaskHandler(baseHandler)

	gctx := globalctx.New()
	workerInstance, err := worker.NewWorkerFromConfig(gctx, cfg, a.model, compositeHandler)
	if err != nil {
		return err
	}
	workerInstance.RegisterTaskHandler(asynctask.NewWorkerControlTaskHandler(workerInstance))
	executor.SetLocalWorker(workerInstance)

	a.mu.Lock()
	if prev, ok := a.workers[name]; ok && prev.gctx != nil {
		prev.gctx.Cancel()
	}
	a.workers[name] = &runtimeWorker{
		workerID: workerID,
		gctx:     gctx,
		handler:  baseHandler,
	}
	a.mu.Unlock()

	go workerInstance.Start()
	return a.waitWorkerOnlineByID(ctx, workerID, true, 2*time.Second)
}

func (a *runtimeActor) StopWorker(ctx context.Context, name string) error {
	a.mu.Lock()
	w, ok := a.workers[name]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("worker %q not found", name)
	}
	if w.gctx != nil {
		w.gctx.Cancel()
	}
	if err := a.waitWorkerOnlineByID(ctx, w.workerID, false, 3*time.Second); err != nil {
		return fmt.Errorf("worker %q did not go offline: %w", name, err)
	}
	return nil
}

func (a *runtimeActor) stopAllWorkers(ctx context.Context, timeout time.Duration) error {
	a.mu.Lock()
	workers := make([]*runtimeWorker, 0, len(a.workers))
	for name, w := range a.workers {
		workers = append(workers, w)
		delete(a.workers, name)
	}
	a.mu.Unlock()

	if len(workers) == 0 {
		return nil
	}

	ids := make([]uuid.UUID, 0, len(workers))
	for _, w := range workers {
		if w == nil {
			continue
		}
		if w.gctx != nil {
			w.gctx.Cancel()
		}
		if w.workerID != uuid.Nil {
			ids = append(ids, w.workerID)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		online, err := a.model.ListOnlineWorkerIDs(ctx, time.Now().Add(-1*time.Minute))
		if err == nil {
			anyOnline := false
			for _, id := range ids {
				if runtimeContainsUUID(online, id) {
					anyOnline = true
					break
				}
			}
			if !anyOnline {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting all workers offline")
}

func (a *runtimeActor) ReleaseWorker(ctx context.Context, name string) error {
	w, err := a.workerByName(name)
	if err != nil {
		return err
	}
	switch h := w.handler.(type) {
	case *smokeWorkerHandler:
		h.release()
	case *blockingWorkerHandler:
		h.release()
	default:
		return fmt.Errorf("worker %q mode does not support release", name)
	}
	return nil
}

func (a *runtimeActor) WaitSignal(ctx context.Context, name string, signal string, timeoutMs int32) error {
	if timeoutMs <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	w, err := a.workerByName(name)
	if err != nil {
		return err
	}
	ch, err := runtimeSignalChannel(w.handler, signal)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(timeoutMs) * time.Millisecond):
		return fmt.Errorf("timeout waiting for signal %q on worker %q", signal, name)
	case <-ch:
		return nil
	}
}

func (a *runtimeActor) WaitTaskLock(ctx context.Context, task string, timeoutMs int32) error {
	taskID, err := a.taskIDByName(ctx, task)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		qtask, err := a.model.GetTaskByID(ctx, taskID)
		if err == nil && qtask.LockedAt != nil && qtask.WorkerID.Valid {
			a.mu.Lock()
			a.lockAnchors[task] = *qtask.LockedAt
			a.mu.Unlock()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("task %q was not locked in time", task)
}

func (a *runtimeActor) WaitLockRefresh(ctx context.Context, task string, timeoutMs int32) error {
	taskID, err := a.taskIDByName(ctx, task)
	if err != nil {
		return err
	}
	a.mu.Lock()
	anchor, ok := a.lockAnchors[task]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("task %q lock anchor is missing; call WaitTaskLock first", task)
	}
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		qtask, err := a.model.GetTaskByID(ctx, taskID)
		if err == nil && qtask.LockedAt != nil && qtask.LockedAt.After(anchor) {
			a.mu.Lock()
			a.lockAnchors[task] = *qtask.LockedAt
			a.mu.Unlock()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("task %q lock was not refreshed", task)
}

func (a *runtimeActor) WaitTaskCompletion(ctx context.Context, task string, timeoutMs int32) error {
	return a.waitTaskState(ctx, task, string(apigen.Completed), true, timeoutMs)
}

func (a *runtimeActor) WaitTaskFailed(ctx context.Context, task string, timeoutMs int32) error {
	return a.waitTaskState(ctx, task, string(apigen.Failed), true, timeoutMs)
}

func (a *runtimeActor) WaitTaskPendingUnlocked(ctx context.Context, task string, timeoutMs int32) error {
	return a.waitTaskState(ctx, task, string(apigen.Pending), true, timeoutMs)
}

func (a *runtimeActor) WaitTaskUnlock(ctx context.Context, task string, timeoutMs int32) error {
	taskID, err := a.taskIDByName(ctx, task)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		qtask, err := a.model.GetTaskByID(ctx, taskID)
		if err == nil && qtask.LockedAt == nil && !qtask.WorkerID.Valid {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("task %q lock was not released", task)
}

func (a *runtimeActor) WaitTaskStartedAfterNow(ctx context.Context, task string, timeoutMs int32) error {
	taskID, err := a.taskIDByName(ctx, task)
	if err != nil {
		return err
	}
	// Add a small skew tolerance so callers can invoke this immediately after
	// asynchronous state transitions without flaking on timing races.
	anchor := time.Now().Add(-200 * time.Millisecond)
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		qtask, err := a.model.GetTaskByID(ctx, taskID)
		if err == nil && qtask.StartedAt != nil && qtask.StartedAt.After(anchor) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("task %q started_at was not updated after now", task)
}

func (a *runtimeActor) WaitNoPendingTasks(ctx context.Context, timeoutMs int32) error {
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		pending, err := a.model.ListAllPendingTasks(ctx)
		if err == nil && len(pending) == 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	pending, err := a.model.ListAllPendingTasks(ctx)
	if err != nil {
		return fmt.Errorf("pending tasks did not drain and list failed: %w", err)
	}
	names := make([]string, 0, len(pending))
	for _, t := range pending {
		name := fmt.Sprintf("id=%d", t.ID)
		if payloadName := taskPayloadName(t.Spec.Payload); payloadName != "" {
			name = payloadName
		}
		names = append(names, name)
	}
	return fmt.Errorf("pending tasks did not drain: count=%d names=%v", len(pending), names)
}

func (a *runtimeActor) ResetCaptured(ctx context.Context) error {
	a.captures.reset()
	return nil
}

func (a *runtimeActor) WaitCapturedCount(ctx context.Context, expected int32, timeoutMs int32) error {
	if expected < 0 {
		return fmt.Errorf("expected must be non-negative")
	}
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if a.captures.count() >= int(expected) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("captured count did not reach %d", expected)
}

func (a *runtimeActor) AssertCapturedCount(ctx context.Context, expected int32) error {
	if expected < 0 {
		return fmt.Errorf("expected must be non-negative")
	}
	got := a.captures.count()
	if got != int(expected) {
		return fmt.Errorf("captured count mismatch: expected=%d got=%d", expected, got)
	}
	return nil
}

func (a *runtimeActor) AssertCapturedContains(ctx context.Context, tasks []string) error {
	records := a.captures.snapshot()
	seen := make(map[string]bool, len(records))
	for _, r := range records {
		seen[r.Task] = true
	}
	for _, task := range tasks {
		if !seen[task] {
			return fmt.Errorf("captured task %q not found", task)
		}
	}
	return nil
}

func (a *runtimeActor) AssertCapturedBefore(ctx context.Context, first string, second string) error {
	records := a.captures.snapshot()
	firstIdx := -1
	secondIdx := -1
	for i, r := range records {
		if firstIdx < 0 && r.Task == first {
			firstIdx = i
		}
		if secondIdx < 0 && r.Task == second {
			secondIdx = i
		}
	}
	if firstIdx < 0 {
		return fmt.Errorf("captured task %q not found", first)
	}
	if secondIdx < 0 {
		return fmt.Errorf("captured task %q not found", second)
	}
	if firstIdx >= secondIdx {
		return fmt.Errorf("captured order mismatch: %q (idx=%d) is not before %q (idx=%d)", first, firstIdx, second, secondIdx)
	}
	return nil
}

func (a *runtimeActor) AssertCapturedByWorkerContains(ctx context.Context, workerName string, tasks []string) error {
	records := a.captures.snapshot()
	seen := map[string]bool{}
	for _, r := range records {
		if r.Worker == workerName {
			seen[r.Task] = true
		}
	}
	for _, task := range tasks {
		if !seen[task] {
			return fmt.Errorf("worker %q did not capture task %q", workerName, task)
		}
	}
	return nil
}

func (a *runtimeActor) AssertContentionCounts(ctx context.Context, expected int32) error {
	return a.contention.assertCounts(int(expected))
}

func (a *runtimeActor) AssertTaskErrorEvent(ctx context.Context, task string, expectedError string) error {
	taskID, err := a.taskIDByName(ctx, task)
	if err != nil {
		return err
	}
	event, err := a.model.GetLastTaskErrorEvent(ctx, taskID)
	if err != nil {
		return err
	}
	if event.Spec.TaskError == nil || event.Spec.TaskError.Error != expectedError {
		return fmt.Errorf("task %q unexpected error event", task)
	}
	return nil
}

func (a *runtimeActor) SetTaskStatus(ctx context.Context, task string, status string) error {
	taskID, err := a.taskIDByName(ctx, task)
	if err != nil {
		return err
	}
	return a.model.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: status,
	})
}

func (a *runtimeActor) SleepMs(ctx context.Context, ms int32) error {
	if ms < 0 {
		return fmt.Errorf("sleep duration must be non-negative")
	}
	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (a *runtimeActor) CreateRuntimeConfig(
	ctx context.Context,
	key string,
	requestID string,
	maxStrictPercentage int32,
	defaultWeight int32,
	w1Weight int32,
	w2Weight int32,
	notify bool,
) error {
	if key == "" {
		return fmt.Errorf("runtime config key is required")
	}
	payload := runtimeConfigPayloadSpec{
		MaxStrictPercentage: int32Ptr(maxStrictPercentage),
		LabelWeights:        map[string]int32{},
	}
	if defaultWeight > 0 {
		payload.LabelWeights["default"] = defaultWeight
	}
	if w1Weight > 0 {
		payload.LabelWeights["w1"] = w1Weight
	}
	if w2Weight > 0 {
		payload.LabelWeights["w2"] = w2Weight
	}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	created, err := a.model.CreateWorkerRuntimeConfig(ctx, payloadRaw)
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.configVersion[key] = created.Version
	a.mu.Unlock()

	if notify {
		return a.notifyRuntimeConfig(ctx, requestID, created.Version)
	}
	return nil
}

func (a *runtimeActor) WaitRuntimeConfigAck(ctx context.Context, key string, requestID string, timeoutMs int32) error {
	if key == "" || requestID == "" {
		return fmt.Errorf("key and requestID are required")
	}
	version, err := a.configVersionByKey(key)
	if err != nil {
		return err
	}

	dsn := smokePostgresDSN()
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := a.notifyRuntimeConfig(ctx, requestID, version); err != nil {
			return err
		}
		waitWindow := 150 * time.Millisecond
		if remaining := time.Until(deadline); remaining < waitWindow {
			waitWindow = remaining
		}
		ack, err := a.waitForRuntimeConfigAck(ctx, dsn, requestID, waitWindow)
		if err == nil {
			if ack.Params.RequestID == requestID && ack.Params.AppliedVersion >= version {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting runtime config ack for request %q", requestID)
}

func (a *runtimeActor) CaptureLatestRuntimeConfigVersion(ctx context.Context, key string) error {
	if key == "" {
		return fmt.Errorf("runtime config key is required")
	}
	latest, err := a.model.GetLatestWorkerRuntimeConfig(ctx)
	if err != nil {
		return err
	}
	captured := latest

	// In some smoke scenarios this method runs concurrently with control-plane update calls.
	// Wait long enough for the update task to be claimed/executed so we don't capture a stale baseline.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		curr, err := a.model.GetLatestWorkerRuntimeConfig(ctx)
		if err == nil && curr.Version > captured.Version {
			captured = curr
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	a.mu.Lock()
	a.configVersion[key] = captured.Version
	a.mu.Unlock()
	return nil
}

func (a *runtimeActor) WaitWorkerLagging(ctx context.Context, workerName string, key string, expected bool, timeoutMs int32) error {
	version, err := a.configVersionByKey(key)
	if err != nil {
		return err
	}
	workerID, err := a.workerIDByName(workerName)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	guardUntil := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		cutoff := time.Now().Add(-1 * time.Minute)
		workers, err := a.model.ListLaggingAliveWorkers(ctx, querier.ListLaggingAliveWorkersParams{
			HeartbeatCutoff: cutoff,
			Version:         version,
		})
		if err == nil {
			matched := runtimeContainsUUID(workers, workerID) == expected
			if matched {
				if expected || time.Now().Before(guardUntil) {
					// For expected=false add a short guard window to let in-flight
					// control-plane update tasks be enqueued/observed.
				} else {
					inflight, inflightErr := a.hasInFlightRuntimeConfigUpdateTask(ctx)
					if inflightErr == nil && !inflight {
						return nil
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("worker %q lagging expected=%v was not reached", workerName, expected)
}

func (a *runtimeActor) hasInFlightRuntimeConfigUpdateTask(ctx context.Context) (bool, error) {
	var count int64
	err := a.model.RunTransactionWithTx(ctx, func(tx core.Tx, _ model.ModelInterface) error {
		return tx.QueryRow(ctx, `
			select count(*)
			from anclax.tasks
			where spec->>'type' in ('updateWorkerRuntimeConfig', 'broadcastUpdateWorkerRuntimeConfig')
			  and status = 'pending'
		`).Scan(&count)
	})
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (a *runtimeActor) WaitWorkerOnline(ctx context.Context, workerName string, expected bool, timeoutMs int32) error {
	workerID, err := a.workerIDByName(workerName)
	if err != nil {
		return err
	}
	if timeoutMs <= 0 {
		timeoutMs = 1000
	}
	if err := a.waitWorkerOnlineByID(ctx, workerID, expected, time.Duration(timeoutMs)*time.Millisecond); err != nil {
		return fmt.Errorf("worker %q online expected=%v was not reached: %w", workerName, expected, err)
	}
	return nil
}

func (a *runtimeActor) waitWorkerOnlineByID(ctx context.Context, workerID uuid.UUID, expected bool, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		cutoff := time.Now().Add(-1 * time.Minute)
		workers, err := a.model.ListOnlineWorkerIDs(ctx, cutoff)
		if err == nil {
			if runtimeContainsUUID(workers, workerID) == expected {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timeout")
}

func (a *runtimeActor) MarkWorkerOffline(ctx context.Context, workerName string) error {
	workerID, err := a.workerIDByName(workerName)
	if err != nil {
		return err
	}
	return a.model.MarkWorkerOffline(ctx, workerID)
}

func (a *runtimeActor) newHandler(workerName string, mode string, taskType string) (worker.TaskHandler, error) {
	switch {
	case mode == "smoke-worker":
		return newSmokeWorkerHandler(), nil
	case mode == "retry":
		return newRetryWorkerHandler(), nil
	case mode == "cron":
		return newCronWorkerHandler(), nil
	case mode == "noop":
		return &noopWorkerHandler{}, nil
	case mode == "blocking":
		return newBlockingWorkerHandler(taskType), nil
	case mode == "signal":
		return newSignalWorkerHandler(taskType), nil
	case mode == "failure":
		return newFailureWorkerHandler(taskType, errors.New("intentional failure")), nil
	case mode == "contention":
		return newRuntimeContentionWorkerHandler(taskType, a.contention), nil
	case mode == "capture":
		return newRuntimeCaptureWorkerHandler(taskType, workerName, a.captures, ""), nil
	case strings.HasPrefix(mode, "capture-fail-once:"):
		task := strings.TrimPrefix(mode, "capture-fail-once:")
		if task == "" {
			return nil, fmt.Errorf("capture-fail-once requires task name")
		}
		return newRuntimeCaptureWorkerHandler(taskType, workerName, a.captures, task), nil
	default:
		return nil, fmt.Errorf("unknown runtime mode %q", mode)
	}
}

func (a *runtimeActor) workerByName(name string) (*runtimeWorker, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w, ok := a.workers[name]
	if !ok {
		return nil, fmt.Errorf("worker %q not found", name)
	}
	return w, nil
}

func (a *runtimeActor) workerIDByName(name string) (uuid.UUID, error) {
	w, err := a.workerByName(name)
	if err != nil {
		return uuid.Nil, err
	}
	return w.workerID, nil
}

func (a *runtimeActor) configVersionByKey(key string) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	version, ok := a.configVersion[key]
	if !ok {
		return 0, fmt.Errorf("runtime config key %q not found", key)
	}
	return version, nil
}

func (a *runtimeActor) notifyRuntimeConfig(ctx context.Context, requestID string, version int64) error {
	notify := runtimeConfigNotifySpec{
		Op: "up_config",
		Params: runtimeConfigNotifyParams{
			RequestID: requestID,
			Version:   version,
		},
	}
	raw, err := json.Marshal(notify)
	if err != nil {
		return err
	}
	return a.model.NotifyWorkerRuntimeConfig(ctx, string(raw))
}

func (a *runtimeActor) waitTaskState(ctx context.Context, task string, status string, unlocked bool, timeoutMs int32) error {
	taskID, err := a.taskIDByName(ctx, task)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		qtask, err := a.model.GetTaskByID(ctx, taskID)
		if err == nil && qtask.Status == status {
			if !unlocked {
				return nil
			}
			if qtask.LockedAt == nil && !qtask.WorkerID.Valid {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("task %q did not reach state %q", task, status)
}

func (a *runtimeActor) taskIDByName(ctx context.Context, task string) (int32, error) {
	var id int32
	err := a.model.RunTransactionWithTx(ctx, func(tx core.Tx, _ model.ModelInterface) error {
		return tx.QueryRow(ctx, "select id from anclax.tasks where spec->'payload'->>'name' = $1 order by created_at desc limit 1", task).Scan(&id)
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (a *runtimeActor) waitForRuntimeConfigAck(ctx context.Context, dsn string, requestID string, timeout time.Duration) (runtimeConfigAckSpec, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return runtimeConfigAckSpec{}, err
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", pgnotify.ChannelRuntimeConfigAck)); err != nil {
		return runtimeConfigAckSpec{}, err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		waitCtx, cancel := context.WithTimeout(ctx, time.Until(deadline))
		notification, err := conn.WaitForNotification(waitCtx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				break
			}
			continue
		}
		if notification == nil {
			continue
		}
		var ack runtimeConfigAckSpec
		if err := json.Unmarshal([]byte(notification.Payload), &ack); err != nil {
			continue
		}
		if ack.Op != "ack" || ack.Params.RequestID != requestID {
			continue
		}
		return ack, nil
	}
	return runtimeConfigAckSpec{}, fmt.Errorf("timeout waiting for runtime config ack")
}

func runtimeSignalChannel(handler any, signal string) (<-chan struct{}, error) {
	switch h := handler.(type) {
	case *smokeWorkerHandler:
		switch signal {
		case "started":
			return h.started(), nil
		case "done":
			return h.done(), nil
		}
	case *retryWorkerHandler:
		switch signal {
		case "first_attempt":
			return h.firstAttempt(), nil
		case "second_attempt":
			return h.secondAttempt(), nil
		}
	case *cronWorkerHandler:
		if signal == "ran" {
			return h.ran(), nil
		}
	case *blockingWorkerHandler:
		switch signal {
		case "started":
			return h.started(), nil
		case "done":
			return h.done(), nil
		}
	case *signalWorkerHandler:
		if signal == "done" {
			return h.done(), nil
		}
	case *failureWorkerHandler:
		if signal == "failed" {
			return h.failed(), nil
		}
	}
	return nil, fmt.Errorf("unsupported signal %q for handler %T", signal, handler)
}

func runtimeContainsUUID(list []uuid.UUID, target uuid.UUID) bool {
	for _, id := range list {
		if id == target {
			return true
		}
	}
	return false
}

type runtimeCaptureRecord struct {
	Worker string
	Task   string
}

type runtimeCaptureTracker struct {
	mu      sync.Mutex
	records []runtimeCaptureRecord
}

func newRuntimeCaptureTracker() *runtimeCaptureTracker {
	return &runtimeCaptureTracker{}
}

func (t *runtimeCaptureTracker) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = nil
}

func (t *runtimeCaptureTracker) record(workerName string, taskName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = append(t.records, runtimeCaptureRecord{
		Worker: workerName,
		Task:   taskName,
	})
}

func (t *runtimeCaptureTracker) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.records)
}

func (t *runtimeCaptureTracker) snapshot() []runtimeCaptureRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]runtimeCaptureRecord, len(t.records))
	copy(out, t.records)
	return out
}

type runtimeCaptureWorkerHandler struct {
	taskType   string
	workerName string
	captures   *runtimeCaptureTracker
	failOnce   map[string]struct{}
}

func newRuntimeCaptureWorkerHandler(taskType string, workerName string, captures *runtimeCaptureTracker, failTask string) *runtimeCaptureWorkerHandler {
	h := &runtimeCaptureWorkerHandler{
		taskType:   taskType,
		workerName: workerName,
		captures:   captures,
		failOnce:   map[string]struct{}{},
	}
	if failTask != "" {
		h.failOnce[failTask] = struct{}{}
	}
	return h
}

func (h *runtimeCaptureWorkerHandler) HandleTask(ctx context.Context, spec worker.Task) error {
	if spec.GetType() != h.taskType {
		return worker.ErrUnknownTaskType
	}
	taskName, err := runtimeTaskNameFromPayload(spec.GetPayload())
	if err != nil {
		return err
	}
	h.captures.record(h.workerName, taskName)
	if _, ok := h.failOnce[taskName]; ok {
		delete(h.failOnce, taskName)
		return errors.New("intentional capture failure")
	}
	return nil
}

func (h *runtimeCaptureWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *runtimeCaptureWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

func runtimeTaskNameFromPayload(payload []byte) (string, error) {
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", err
	}
	nameRaw, ok := decoded["name"]
	if !ok {
		return "", fmt.Errorf("payload missing name")
	}
	name, ok := nameRaw.(string)
	if !ok || name == "" {
		return "", fmt.Errorf("payload name must be non-empty string")
	}
	return name, nil
}

func taskPayloadName(payload []byte) string {
	name, err := runtimeTaskNameFromPayload(payload)
	if err != nil {
		return ""
	}
	return name
}

type runtimeContentionPayload struct {
	ID int `json:"id"`
}

type runtimeContentionTracker struct {
	mu   sync.Mutex
	seen map[int]int
}

func newRuntimeContentionTracker() *runtimeContentionTracker {
	return &runtimeContentionTracker{
		seen: make(map[int]int),
	}
}

func (t *runtimeContentionTracker) record(id int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seen[id]++
}

func (t *runtimeContentionTracker) assertCounts(expected int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := 1; i <= expected; i++ {
		if t.seen[i] != 1 {
			return fmt.Errorf("task %d processed %d times", i, t.seen[i])
		}
	}
	return nil
}

type runtimeContentionWorkerHandler struct {
	taskType string
	tracker  *runtimeContentionTracker
}

func newRuntimeContentionWorkerHandler(taskType string, tracker *runtimeContentionTracker) *runtimeContentionWorkerHandler {
	return &runtimeContentionWorkerHandler{
		taskType: taskType,
		tracker:  tracker,
	}
}

func (h *runtimeContentionWorkerHandler) HandleTask(ctx context.Context, spec worker.Task) error {
	if spec.GetType() != h.taskType {
		return worker.ErrUnknownTaskType
	}
	var payload runtimeContentionPayload
	if err := json.Unmarshal(spec.GetPayload(), &payload); err != nil {
		return err
	}
	h.tracker.record(payload.ID)
	return nil
}

func (h *runtimeContentionWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *runtimeContentionWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}
