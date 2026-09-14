package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
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

// The shared manager renews in bounded batches and enforces each attempt's
// last confirmed deadline. Removing an attempt drains any in-flight renewal.
func (p *ModelPort) startLockRefresh(ctx context.Context, task Task) context.CancelFunc {
	if p.leases == nil {
		return func() {}
	}
	return p.leases.register(ctx, task)
}

func (p *ModelPort) taskInterruptCause(ctx context.Context) error {
	cause := context.Cause(ctx)
	if errors.Is(cause, context.Canceled) {
		return taskcore.ErrTaskInterrupted
	}
	return cause
}
