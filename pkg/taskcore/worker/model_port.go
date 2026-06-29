package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	"go.uber.org/zap"
)

var errSkipFinalize = errors.New("skip finalize")

type ModelPort struct {
	model model.ModelInterface

	workerID      uuid.UUID
	workerIDParam uuid.NullUUID
	labels        []string
	hasLabels     bool
	labelsJSON    json.RawMessage

	lockTTL             time.Duration
	lockRefreshInterval time.Duration
	lifeCycleHandler    TaskLifeCycleHandlerInterface
	taskHandler         TaskHandler
	now                 func() time.Time

	taskRuntimeMu      sync.Mutex
	taskRuntimeEntries map[int32]*taskRuntimeEntry
}

type taskRuntimeEntry struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

func newTaskRuntimeEntry(cancel context.CancelCauseFunc) *taskRuntimeEntry {
	return &taskRuntimeEntry{
		cancel: cancel,
		done:   make(chan struct{}),
	}
}

func (e *taskRuntimeEntry) interrupt(cause error) {
	e.cancel(cause)
}

func (e *taskRuntimeEntry) wait(ctx context.Context) error {
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func NewModelPort(
	m model.ModelInterface,
	workerID uuid.UUID,
	labels []string,
	taskHandler TaskHandler,
	lockTTL time.Duration,
	lockRefreshInterval time.Duration,
) (*ModelPort, error) {
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return nil, fmt.Errorf("marshal worker labels: %w", err)
	}
	return &ModelPort{
		model:               m,
		workerID:            workerID,
		workerIDParam:       uuid.NullUUID{UUID: workerID, Valid: true},
		labels:              append([]string(nil), labels...),
		hasLabels:           hasUserClaimLabels(labels),
		labelsJSON:          labelsJSON,
		lockTTL:             lockTTL,
		lockRefreshInterval: lockRefreshInterval,
		lifeCycleHandler:    NewTaskLifeCycleHandler(m, taskHandler, workerID),
		taskHandler:         taskHandler,
		now:                 time.Now,
		taskRuntimeEntries:  make(map[int32]*taskRuntimeEntry),
	}, nil
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
	return nil
}

func (p *ModelPort) MarkWorkerOffline(ctx context.Context, workerID string) error {
	if err := p.model.MarkWorkerOffline(ctx, p.workerID); err != nil {
		return fmt.Errorf("mark worker offline: %w", err)
	}
	return nil
}

func (p *ModelPort) ClaimStrict(ctx context.Context, req ClaimRequest) (*Task, error) {
	lockExpiry := p.now().Add(-p.lockTTL)
	var out *Task
	err := p.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		qtask, err := txm.ClaimStrictTask(ctx, querier.ClaimStrictTaskParams{
			WorkerID:   p.workerIDParam,
			LockExpiry: &lockExpiry,
			Labels:     p.labels,
			HasLabels:  p.hasLabels,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		out = taskFromQuerier(qtask)
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
	lockExpiry := p.now().Add(-p.lockTTL)
	var out *Task
	err := p.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		qtask, err := txm.ClaimNormalTaskByGroup(ctx, querier.ClaimNormalTaskByGroupParams{
			WorkerID:       p.workerIDParam,
			LockExpiry:     &lockExpiry,
			Labels:         p.labels,
			HasLabels:      p.hasLabels,
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
	lockExpiry := p.now().Add(-p.lockTTL)
	var out *Task
	err := p.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		qtask, err := txm.ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{
			ID:         taskID,
			WorkerID:   p.workerIDParam,
			LockExpiry: &lockExpiry,
			Labels:     p.labels,
			HasLabels:  p.hasLabels,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		out = taskFromQuerier(qtask)
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

func (p *ModelPort) ExecuteTask(ctx context.Context, task Task) error {
	baseCtx, baseCancel := context.WithCancelCause(ctx)
	p.registerTaskRuntime(task.ID, baseCancel)
	defer func() {
		baseCancel(nil)
	}()

	execCtx, cancel, err := p.withTaskTimeout(baseCtx, task)
	if err != nil {
		return err
	}
	defer cancel()

	refreshCancel := p.startLockRefresh(execCtx, task.ID)
	defer refreshCancel()

	apiTask := taskToAPI(task)
	if err := p.model.RunTransactionWithTx(execCtx, func(tx core.Tx, txm model.ModelInterface) error {
		return p.lifeCycleHandler.HandleAttributes(execCtx, tx, apiTask)
	}); err != nil {
		if interruptErr := p.taskInterruptCause(execCtx); interruptErr != nil {
			return interruptErr
		}
		if errors.Is(err, taskcore.ErrTaskLockLost) {
			return errSkipFinalize
		}
		return fmt.Errorf("handle task attributes: %w", err)
	}

	if p.taskHandler == nil {
		if err := p.taskInterruptCause(execCtx); err != nil {
			return err
		}
		return nil
	}

	err = p.taskHandler.HandleTask(execCtx, task)
	if err != nil {
		if interruptErr := p.taskInterruptCause(execCtx); interruptErr != nil {
			return interruptErr
		}
		return err
	}
	if err := p.taskInterruptCause(execCtx); err != nil {
		return err
	}
	return nil
}

func (p *ModelPort) FinalizeTask(ctx context.Context, task Task, execErr error) error {
	defer p.completeTaskRuntime(task.ID)

	if errors.Is(execErr, errSkipFinalize) {
		return nil
	}
	if errors.Is(execErr, taskcore.ErrTaskInterrupted) {
		if err := p.releaseTaskLock(ctx, task.ID); err != nil {
			if errors.Is(err, taskcore.ErrTaskLockLost) {
				return nil
			}
			return fmt.Errorf("finalize interrupted task: %w", err)
		}
		return nil
	}

	apiTask := taskToAPI(task)
	if execErr != nil {
		err := p.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
			return p.lifeCycleHandler.HandleFailed(ctx, tx, apiTask, execErr)
		})
		if errors.Is(err, taskcore.ErrTaskLockLost) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("finalize failed task: %w", err)
		}
		return nil
	}

	err := p.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		return p.lifeCycleHandler.HandleCompleted(ctx, tx, apiTask)
	})
	if errors.Is(err, taskcore.ErrTaskLockLost) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("finalize completed task: %w", err)
	}
	return nil
}

func (p *ModelPort) Heartbeat(ctx context.Context, workerID string) error {
	if _, err := p.model.UpdateWorkerHeartbeat(ctx, p.workerID); err != nil {
		return fmt.Errorf("update worker heartbeat: %w", err)
	}
	return nil
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

func (p *ModelPort) InterruptTask(taskID int32, cause error) {
	entry := p.taskRuntimeEntry(taskID)
	if entry != nil {
		entry.interrupt(cause)
	}
}

func (p *ModelPort) WaitTaskRuntimes(ctx context.Context, taskIDs []int32) error {
	entries := p.taskRuntimeEntriesFor(taskIDs)
	for _, entry := range entries {
		if err := entry.wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (p *ModelPort) registerTaskRuntime(taskID int32, cancel context.CancelCauseFunc) {
	p.taskRuntimeMu.Lock()
	defer p.taskRuntimeMu.Unlock()
	if _, exists := p.taskRuntimeEntries[taskID]; exists {
		log.Error("duplicate task runtime entry", zap.Int32("task_id", taskID), zap.String("worker_id", p.workerID.String()))
		panic(fmt.Sprintf("duplicate task runtime entry for task %d", taskID))
	}
	p.taskRuntimeEntries[taskID] = newTaskRuntimeEntry(cancel)
}

func (p *ModelPort) completeTaskRuntime(taskID int32) {
	p.taskRuntimeMu.Lock()
	entry := p.taskRuntimeEntries[taskID]
	if entry != nil {
		close(entry.done)
		delete(p.taskRuntimeEntries, taskID)
	}
	p.taskRuntimeMu.Unlock()
}

func (p *ModelPort) taskRuntimeEntry(taskID int32) *taskRuntimeEntry {
	p.taskRuntimeMu.Lock()
	entry := p.taskRuntimeEntries[taskID]
	p.taskRuntimeMu.Unlock()
	return entry
}

func (p *ModelPort) taskRuntimeEntriesFor(taskIDs []int32) []*taskRuntimeEntry {
	p.taskRuntimeMu.Lock()
	defer p.taskRuntimeMu.Unlock()
	entries := make([]*taskRuntimeEntry, 0, len(taskIDs))
	seen := make(map[*taskRuntimeEntry]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		entry := p.taskRuntimeEntries[taskID]
		if entry == nil {
			continue
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		entries = append(entries, entry)
	}
	return entries
}

func (p *ModelPort) withTaskTimeout(ctx context.Context, task Task) (context.Context, context.CancelFunc, error) {
	if task.Attributes.Timeout == nil {
		c, cancel := context.WithCancel(ctx)
		return c, cancel, nil
	}
	timeout, err := time.ParseDuration(*task.Attributes.Timeout)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse timeout: %w", err)
	}
	c, cancel := context.WithTimeout(ctx, timeout)
	return c, cancel, nil
}

func (p *ModelPort) startLockRefresh(ctx context.Context, taskID int32) context.CancelFunc {
	if p.lockRefreshInterval <= 0 {
		return func() {}
	}
	refreshCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(p.lockRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-ticker.C:
				if _, err := p.model.RefreshTaskLock(refreshCtx, querier.RefreshTaskLockParams{
					ID:       taskID,
					WorkerID: p.workerIDParam,
				}); err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						p.InterruptTask(taskID, p.taskInterruptCauseFromStore(refreshCtx, taskID))
						return
					}
				}
			}
		}
	}()
	return cancel
}

func (p *ModelPort) taskInterruptCauseFromStore(ctx context.Context, taskID int32) error {
	task, err := p.model.GetTaskByID(ctx, taskID)
	if err != nil {
		return taskcore.ErrTaskInterrupted
	}
	switch apigen.TaskStatus(task.Status) {
	case apigen.Paused:
		return taskcore.ErrTaskPaused
	case apigen.Cancelled:
		return taskcore.ErrTaskCancelled
	default:
		return taskcore.ErrTaskInterrupted
	}
}

func hasUserClaimLabels(labels []string) bool {
	for _, label := range labels {
		if !strings.HasPrefix(label, "worker:") {
			return true
		}
	}
	return false
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
		ID:         apiTask.ID,
		Priority:   priority,
		Attempts:   apiTask.Attempts,
		Attributes: apiTask.Attributes,
		Spec:       apiTask.Spec,
	}
}

func taskToAPI(task Task) apigen.Task {
	return apigen.Task{
		ID:         task.ID,
		Attempts:   task.Attempts,
		Attributes: task.Attributes,
		Spec:       task.Spec,
	}
}

func (p *ModelPort) updateTaskStatusByWorker(ctx context.Context, taskID int32, status apigen.TaskStatus) error {
	return p.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		if _, err := txm.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
			ID:       taskID,
			Status:   string(status),
			WorkerID: p.workerIDParam,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return taskcore.ErrTaskLockLost
			}
			return err
		}
		return nil
	})
}

func (p *ModelPort) releaseTaskLock(ctx context.Context, taskID int32) error {
	return p.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		if _, err := txm.ReleaseTaskLockByWorker(ctx, querier.ReleaseTaskLockByWorkerParams{
			ID:       taskID,
			WorkerID: p.workerIDParam,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return taskcore.ErrTaskLockLost
			}
			return err
		}
		return nil
	})
}

func (p *ModelPort) taskInterruptCause(ctx context.Context) error {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, taskcore.ErrTaskCancelled):
		return taskcore.ErrTaskCancelled
	case errors.Is(cause, taskcore.ErrTaskPaused):
		return taskcore.ErrTaskPaused
	case errors.Is(cause, taskcore.ErrTaskInterrupted):
		return taskcore.ErrTaskInterrupted
	default:
		return nil
	}
}
