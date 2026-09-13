package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
)

type executionKey struct {
	taskID       int32
	leaseVersion int64
}

func (t Task) executionKey() executionKey { return executionKey{t.ID, t.LeaseVersion} }

type taskRuntimeEntry struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

func newTaskRuntimeEntry(cancel context.CancelCauseFunc) *taskRuntimeEntry {
	return &taskRuntimeEntry{cancel: cancel, done: make(chan struct{})}
}
func (e *taskRuntimeEntry) interrupt(cause error) { e.cancel(cause) }
func (e *taskRuntimeEntry) wait(ctx context.Context) error {
	select {
	case <-e.done:
		return nil
	default:
	}
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *ModelPort) registerTaskRuntime(task Task, cancel context.CancelCauseFunc) {
	p.taskRuntimeMu.Lock()
	defer p.taskRuntimeMu.Unlock()
	key := task.executionKey()
	if _, exists := p.taskRuntimeEntries[key]; exists {
		panic(fmt.Sprintf("duplicate task attempt %v", key))
	}
	p.taskRuntimeEntries[key] = newTaskRuntimeEntry(cancel)
}

func (p *ModelPort) completeTaskRuntime(task Task) {
	p.taskRuntimeMu.Lock()
	defer p.taskRuntimeMu.Unlock()
	key := task.executionKey()
	if entry := p.taskRuntimeEntries[key]; entry != nil {
		close(entry.done)
		delete(p.taskRuntimeEntries, key)
	}
}

func (p *ModelPort) taskRuntimeEntriesFor(taskIDs []int32) []*taskRuntimeEntry {
	p.taskRuntimeMu.Lock()
	defer p.taskRuntimeMu.Unlock()
	ids := make(map[int32]bool, len(taskIDs))
	for _, id := range taskIDs {
		ids[id] = true
	}
	var entries []*taskRuntimeEntry
	for key, entry := range p.taskRuntimeEntries {
		if ids[key.taskID] {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (p *ModelPort) InterruptTask(taskID int32, cause error) {
	for _, entry := range p.taskRuntimeEntriesFor([]int32{taskID}) {
		entry.interrupt(cause)
	}
}

func (p *ModelPort) interruptAttempt(task Task, cause error) {
	p.taskRuntimeMu.Lock()
	entry := p.taskRuntimeEntries[task.executionKey()]
	p.taskRuntimeMu.Unlock()
	if entry != nil {
		entry.interrupt(cause)
	}
}

func (p *ModelPort) TaskRuntimesActive(taskIDs []int32) bool {
	return len(p.taskRuntimeEntriesFor(taskIDs)) != 0
}
func (p *ModelPort) WaitTaskRuntimes(ctx context.Context, taskIDs []int32) error {
	for _, entry := range p.taskRuntimeEntriesFor(taskIDs) {
		if err := entry.wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (p *ModelPort) withTaskTimeout(ctx context.Context, task Task) (context.Context, context.CancelFunc, error) {
	if task.Attributes.Timeout == nil {
		c, cancel := context.WithCancel(ctx)
		return c, cancel, nil
	}
	timeout, err := time.ParseDuration(*task.Attributes.Timeout)
	if err != nil || timeout <= 0 {
		return nil, nil, fmt.Errorf("invalid task timeout %q", *task.Attributes.Timeout)
	}
	c, cancel := context.WithTimeout(ctx, timeout)
	return c, cancel, nil
}

// Renewals are bounded by the last confirmed lease, including transient DB
// failures. Stopping renewal waits for its goroutine before finalization starts.
func (p *ModelPort) startLockRefresh(ctx context.Context, task Task) context.CancelFunc {
	if p.lockRefreshInterval <= 0 {
		return func() {}
	}
	refreshCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(p.lockRefreshInterval)
		defer ticker.Stop()
		lastConfirmed := task.claimedAt
		if lastConfirmed.IsZero() {
			lastConfirmed = time.Now()
		}
		leaseDeadline := time.NewTimer(max(0, p.lockTTL-time.Since(lastConfirmed)))
		defer leaseDeadline.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-leaseDeadline.C:
				p.interruptAttempt(task, taskcore.ErrTaskLockLost)
				return
			case <-ticker.C:
				remaining := p.lockTTL - time.Since(lastConfirmed)
				if remaining <= 0 {
					p.interruptAttempt(task, taskcore.ErrTaskLockLost)
					return
				}
				callCtx, callCancel := context.WithTimeout(refreshCtx, remaining)
				started := time.Now()
				_, err := p.model.RefreshTaskLock(callCtx, querier.RefreshTaskLockParams{ID: task.ID, WorkerID: p.workerIDParam, LeaseVersion: task.LeaseVersion})
				callCancel()
				if err == nil {
					lastConfirmed = started
					leaseDeadline.Reset(max(0, p.lockTTL-time.Since(lastConfirmed)))
					continue
				}
				if refreshCtx.Err() != nil {
					return
				}
				if errors.Is(err, pgx.ErrNoRows) {
					lookupCtx, lookupCancel := context.WithTimeout(refreshCtx, p.lockRefreshInterval)
					cause := p.taskInterruptCauseFromStore(lookupCtx, task.ID)
					lookupCancel()
					p.interruptAttempt(task, cause)
					return
				}
				if time.Since(lastConfirmed) >= p.lockTTL {
					p.interruptAttempt(task, taskcore.ErrTaskLockLost)
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (p *ModelPort) taskInterruptCauseFromStore(ctx context.Context, taskID int32) error {
	task, err := p.model.GetTaskByID(ctx, taskID)
	if err != nil {
		return taskcore.ErrTaskLockLost
	}
	switch apigen.TaskStatus(task.Status) {
	case apigen.Paused:
		return taskcore.ErrTaskPaused
	case apigen.Cancelled:
		return taskcore.ErrTaskCancelled
	default:
		return taskcore.ErrTaskLockLost
	}
}

func (p *ModelPort) taskInterruptCause(ctx context.Context) error {
	cause := context.Cause(ctx)
	if errors.Is(cause, context.Canceled) {
		return taskcore.ErrTaskInterrupted
	}
	return cause
}
