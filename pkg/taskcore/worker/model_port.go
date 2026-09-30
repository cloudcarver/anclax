package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/cloudcarver/anclax/core"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/types"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errSkipFinalize = errors.New("skip finalize")

type ModelPort struct {
	model model.ModelInterface

	workerID      uuid.UUID
	workerIDParam uuid.NullUUID
	labels        []string
	labelsJSON    json.RawMessage

	lockTTL             time.Duration
	lockRefreshInterval time.Duration
	lifeCycleHandler    TaskLifeCycleHandlerInterface
	taskHandler         TaskHandler
	leases              *leaseManager

	taskRuntimeMu      sync.Mutex
	taskRuntimeEntries map[executionKey]*taskRuntimeEntry

	concurrencyMaintenanceMu sync.Mutex
	nextConcurrencySweep     time.Time
	prefetchCapacity         int32
	prefetchStrictPercentage int32
	prefetchHeartbeatTTL     int64
}

func NewModelPort(
	m model.ModelInterface,
	workerID uuid.UUID,
	labels []string,
	taskHandler TaskHandler,
	lockTTL time.Duration,
	lockRefreshInterval time.Duration,
) (*ModelPort, error) {
	if lockTTL < time.Millisecond {
		return nil, fmt.Errorf("lock TTL must be at least one millisecond")
	}
	if lockRefreshInterval < 0 || lockRefreshInterval >= lockTTL {
		return nil, fmt.Errorf("lock refresh interval must be non-negative and less than lock TTL")
	}
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return nil, fmt.Errorf("marshal worker labels: %w", err)
	}
	p := &ModelPort{
		model:               m,
		workerID:            workerID,
		workerIDParam:       uuid.NullUUID{UUID: workerID, Valid: true},
		labels:              append([]string(nil), labels...),
		labelsJSON:          labelsJSON,
		lockTTL:             lockTTL,
		lockRefreshInterval: lockRefreshInterval,
		lifeCycleHandler:    NewTaskLifeCycleHandler(m, taskHandler, workerID),
		taskHandler:         taskHandler,
		taskRuntimeEntries:  make(map[executionKey]*taskRuntimeEntry),
	}
	if lockRefreshInterval > 0 {
		var q leaseQueries = m
		concurrency := 10
		if provider, ok := m.(interface{ TaskLeaseQueries() querier.Querier }); ok {
			q = provider.TaskLeaseQueries()
			if q == nil {
				return nil, fmt.Errorf("task lease renewal requires a pool-backed model")
			}
		}
		if provider, ok := m.(interface{ TaskLeasePoolStats() *pgxpool.Stat }); ok {
			concurrency = int(provider.TaskLeasePoolStats().MaxConns())
		}
		p.leases = newLeaseManager(q, p.workerIDParam, lockTTL, lockRefreshInterval, concurrency, p.interruptAttempt)
	}
	return p, nil
}

func (p *ModelPort) RegisterWorker(ctx context.Context, workerID string, labels []string, appliedConfigVersion int64) error {
	_, err := p.model.UpsertWorker(ctx, querier.UpsertWorkerParams{
		ID:                   p.workerID,
		Labels:               p.labelsJSON,
		AppliedConfigVersion: appliedConfigVersion,
	})
	if err != nil {
		return fmt.Errorf("register worker: %w", err)
	}
	if p.prefetchCapacity > 0 {
		if err := p.model.ConfigureWorkerPrefetch(ctx, querier.ConfigureWorkerPrefetchParams{
			WorkerID: p.workerID, Capacity: p.prefetchCapacity, StrictPercentage: p.prefetchStrictPercentage, HeartbeatTtlMs: p.prefetchHeartbeatTTL,
		}); err != nil {
			return err
		}
		return p.model.EnsureTaskPrefetch(ctx)
	}
	return nil
}

func (p *ModelPort) MarkWorkerOffline(ctx context.Context, workerID string) error {
	if err := p.model.MarkWorkerOffline(ctx, p.workerID); err != nil {
		return fmt.Errorf("mark worker offline: %w", err)
	}
	return nil
}

func (p *ModelPort) ClaimStrict(ctx context.Context, req ClaimRequest) (*Task, error) {
	if err := p.maintainConcurrency(ctx); err != nil {
		return nil, err
	}
	claimedAt := time.Now()
	var out *Task
	err := p.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		qtask, err := txm.ClaimStrictTask(ctx, querier.ClaimStrictTaskParams{
			WorkerID:  p.workerIDParam,
			LockTtlMs: p.lockTTL.Milliseconds(),
			Labels:    p.labels,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		out = taskFromQuerier(qtask)
		out.claimedAt = claimedAt
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claim strict task: %w", err)
	}
	if out == nil {
		return nil, ErrNoTask
	}
	return out, nil
}

func (p *ModelPort) ClaimNormalByGroup(ctx context.Context, req ClaimNormalRequest) (*Task, error) {
	if err := p.maintainConcurrency(ctx); err != nil {
		return nil, err
	}
	claimedAt := time.Now()
	var out *Task
	err := p.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		qtask, err := txm.ClaimNormalTaskByGroup(ctx, querier.ClaimNormalTaskByGroupParams{
			WorkerID:       p.workerIDParam,
			AllowStrict:    req.AllowStrict,
			LockTtlMs:      p.lockTTL.Milliseconds(),
			Labels:         p.labels,
			GroupName:      req.Group,
			WeightedLabels: append([]string(nil), req.WeightedLabels...),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		out = taskFromQuerier(qtask)
		out.claimedAt = claimedAt
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claim normal task: %w", err)
	}
	if out == nil {
		return nil, ErrNoTask
	}
	return out, nil
}

func (p *ModelPort) ClaimByID(ctx context.Context, taskID int32, req ClaimRequest) (*Task, error) {
	if err := p.maintainConcurrency(ctx); err != nil {
		return nil, err
	}
	claimedAt := time.Now()
	var out *Task
	err := p.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		qtask, err := txm.ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{
			ID:          taskID,
			AllowStrict: req.AllowStrict,
			WorkerID:    p.workerIDParam,
			LockTtlMs:   p.lockTTL.Milliseconds(),
			Labels:      p.labels,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		out = taskFromQuerier(qtask)
		out.claimedAt = claimedAt
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claim task by id: %w", err)
	}
	if out == nil {
		return nil, ErrNoTask
	}
	return out, nil
}

func (p *ModelPort) ExecuteTask(ctx context.Context, task Task) (execErr error) {
	baseCtx, baseCancel := context.WithCancelCause(ctx)
	p.registerTaskRuntime(task, baseCancel)
	defer baseCancel(nil)
	defer func() {
		if value := recover(); value != nil {
			execErr = fmt.Errorf("task handler panic: %v\n%s", value, debug.Stack())
		}
	}()
	execCtx, cancel, err := p.withTaskTimeout(baseCtx, task)
	if err != nil {
		return err
	}
	defer cancel()
	status, err := p.model.GetTaskAttemptStatus(execCtx, querier.GetTaskAttemptStatusParams{
		ID: task.ID, WorkerID: p.workerIDParam, LeaseVersion: task.LeaseVersion,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return taskcore.ErrTaskLockLost
	}
	if err != nil {
		return err
	}
	switch apigen.TaskStatus(status) {
	case apigen.Paused:
		return taskcore.ErrTaskPaused
	case apigen.Cancelled:
		return taskcore.ErrTaskCancelled
	case apigen.Pending, apigen.TaskStatus("running"):
	default:
		return taskcore.ErrTaskLockLost
	}
	if !task.claimedAt.IsZero() && time.Since(task.claimedAt) >= p.lockTTL {
		return taskcore.ErrTaskLockLost
	}
	p.renewThroughFinalization(execCtx, task)
	if task.GetType() == PrefetchTaskType {
		execErr = p.executePrefetch(execCtx, task)
	} else if p.taskHandler != nil {
		execErr = p.taskHandler.HandleTask(execCtx, task)
	}
	if cause := p.taskInterruptCause(execCtx); cause != nil {
		return cause
	}
	return execErr
}

func (p *ModelPort) FinalizeTask(ctx context.Context, task Task, execErr error) (resultErr error) {
	started := time.Now()
	defer func() { observeScheduler("finalize", started, resultErr) }()
	defer p.completeTaskRuntime(task)
	if errors.Is(execErr, errSkipFinalize) || errors.Is(execErr, taskcore.ErrTaskLockLost) {
		return nil
	}
	err := p.finalizeWithRetry(ctx, task, execErr)
	if errors.Is(err, taskcore.ErrTaskLockLost) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("finalize task attempt: %w", err)
	}
	return nil
}

func (p *ModelPort) Heartbeat(ctx context.Context, workerID string) error {
	if _, err := p.model.UpdateWorkerHeartbeat(ctx, p.workerID); err != nil {
		return fmt.Errorf("update worker heartbeat: %w", err)
	}
	if p.prefetchCapacity > 0 {
		return nil
	}
	return p.maintainConcurrency(ctx)
}

func (p *ModelPort) RefreshRuntimeConfig(ctx context.Context, workerID string, requestID string) (*RuntimeConfig, error) {
	cfg, err := p.model.GetLatestWorkerRuntimeConfig(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get latest runtime config: %w", err)
	}

	decoded, err := decodeRuntimeConfigPayload(cfg.Payload)
	if err != nil {
		return nil, err
	}
	return &RuntimeConfig{
		Version:             cfg.Version,
		MaxStrictPercentage: decoded.MaxStrictPercentage,
		LabelWeights:        decoded.LabelWeights,
	}, nil
}

func (p *ModelPort) AckRuntimeConfigApplied(ctx context.Context, workerID string, requestID string, appliedVersion int64) error {
	if err := p.model.UpdateWorkerAppliedConfigVersion(ctx, querier.UpdateWorkerAppliedConfigVersionParams{
		ID:                   p.workerID,
		AppliedConfigVersion: appliedVersion,
	}); err != nil {
		return fmt.Errorf("update worker applied config version: %w", err)
	}
	return nil
}

func decodeRuntimeConfigPayload(raw json.RawMessage) (RuntimeConfigPayload, error) {
	var payload RuntimeConfigPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return RuntimeConfigPayload{}, fmt.Errorf("unmarshal worker runtime config payload: %w", err)
	}
	return payload, nil
}

func taskFromQuerier(task *querier.AnclaxTask) *Task {
	if task == nil {
		return nil
	}
	apiTask := types.TaskToAPI(task)
	priority := int32(0)
	if apiTask.Attributes.Priority != nil {
		priority = *apiTask.Attributes.Priority
	}
	return &Task{
		ID:           apiTask.ID,
		LeaseVersion: task.LeaseVersion,
		Priority:     priority,
		Attempts:     apiTask.Attempts,
		Attributes:   apiTask.Attributes,
		Spec:         apiTask.Spec,
	}
}

// LookupTask supplies admission metadata without claiming a lease.
func (p *ModelPort) LookupTask(ctx context.Context, id int32) (*Task, error) {
	task, err := p.model.GetTaskByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoTask
	}
	if err != nil {
		return nil, err
	}
	return taskFromQuerier(task), nil
}

func (p *ModelPort) ClaimControl(ctx context.Context, req ClaimRequest) (*Task, error) {
	claimedAt := time.Now()
	var out *Task
	err := p.model.RunTransactionWithTx(ctx, func(_ core.Tx, txm model.ModelInterface) error {
		task, err := txm.ClaimWorkerCommand(ctx, querier.ClaimWorkerCommandParams{
			WorkerID: p.workerIDParam, Labels: p.labels, LockTtlMs: p.lockTTL.Milliseconds(),
		})
		if err != nil {
			return err
		}
		out = taskFromQuerier(task)
		out.claimedAt = claimedAt
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoTask
	}
	return out, err
}

// Sweep outside the claim transaction. A busy or full queue must not prevent
// recovery of dead attempts, including paused/cancelled ones. Concurrent
// admission calls share one sweep at most every 250 ms per worker.
func (p *ModelPort) maintainConcurrency(ctx context.Context) error {
	if !p.concurrencyMaintenanceMu.TryLock() {
		return nil
	}
	defer p.concurrencyMaintenanceMu.Unlock()
	if time.Now().Before(p.nextConcurrencySweep) {
		return nil
	}
	if err := p.model.MaintainTaskConcurrency(ctx, p.lockTTL.Milliseconds()); err != nil {
		return fmt.Errorf("maintain task concurrency: %w", err)
	}
	p.nextConcurrencySweep = time.Now().Add(250 * time.Millisecond)
	return nil
}
