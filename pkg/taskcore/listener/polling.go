package listener

import (
	"context"
	"sync"
	"time"

	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
)

const defaultPollInterval = time.Second

type PollingTaskEventListener struct {
	model model.ModelInterface

	mu       sync.RWMutex
	watchers map[int32]map[*subscription]struct{}

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

type subscription struct {
	ch   chan TaskTerminalEvent
	done chan struct{}
	once sync.Once
}

func NewPollingTaskEventListener(model model.ModelInterface) *PollingTaskEventListener {
	ctx, cancel := context.WithCancel(context.Background())
	l := &PollingTaskEventListener{
		model:    model,
		watchers: map[int32]map[*subscription]struct{}{},
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	go l.loop()
	return l
}

func (l *PollingTaskEventListener) Close(ctx context.Context) error {
	l.cancel()
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *PollingTaskEventListener) WaitTask(ctx context.Context, taskID int32) (<-chan TaskTerminalEvent, error) {
	status, err := l.taskStatusByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if IsTerminalStatus(status) {
		ch := make(chan TaskTerminalEvent, 1)
		ch <- TaskTerminalEvent{TaskID: taskID, Status: status}
		close(ch)
		return ch, nil
	}

	sub := &subscription{
		ch:   make(chan TaskTerminalEvent, 1),
		done: make(chan struct{}),
	}
	if err := l.register(taskID, sub); err != nil {
		return nil, err
	}

	go func() {
		select {
		case <-ctx.Done():
			l.unregister(taskID, sub)
		case <-sub.done:
		}
	}()

	return sub.ch, nil
}

func (l *PollingTaskEventListener) taskStatusByID(ctx context.Context, taskID int32) (apigen.TaskStatus, error) {
	task, err := l.model.GetTaskWaitStatusByID(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrTaskNotFound
		}
		return "", errors.Wrap(err, "get task wait status")
	}
	return apigen.TaskStatus(task.Status), nil
}

func (l *PollingTaskEventListener) register(taskID int32, sub *subscription) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.ctx.Done():
		return ErrListenerClosed
	default:
	}
	if _, ok := l.watchers[taskID]; !ok {
		l.watchers[taskID] = map[*subscription]struct{}{}
	}
	l.watchers[taskID][sub] = struct{}{}
	return nil
}

func (l *PollingTaskEventListener) unregister(taskID int32, sub *subscription) {
	l.removeSubscription(taskID, sub, TaskTerminalEvent{}, false)
}

func (l *PollingTaskEventListener) loop() {
	defer close(l.done)
	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-l.ctx.Done():
			l.failAll(ErrListenerClosed)
			return
		case <-ticker.C:
			l.poll(l.ctx)
		}
	}
}

func (l *PollingTaskEventListener) poll(ctx context.Context) {
	ids, snapshot := l.snapshot()
	if len(ids) == 0 {
		return
	}

	start := time.Now()
	rows, err := l.model.ListTerminalTaskWaitStatuses(ctx, ids)
	metrics.TaskListenerPollDurationSeconds.Observe(time.Since(start).Seconds())
	if err != nil {
		l.failSnapshot(snapshot, errors.Wrap(err, "list terminal task wait statuses"))
		return
	}

	for _, row := range rows {
		l.notify(row.ID, apigen.TaskStatus(row.Status))
	}
}

func (l *PollingTaskEventListener) snapshot() ([]int32, map[int32][]*subscription) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.watchers) == 0 {
		return nil, nil
	}
	ids := make([]int32, 0, len(l.watchers))
	subs := make(map[int32][]*subscription, len(l.watchers))
	for taskID, watchers := range l.watchers {
		ids = append(ids, taskID)
		for sub := range watchers {
			subs[taskID] = append(subs[taskID], sub)
		}
	}
	return ids, subs
}

func (l *PollingTaskEventListener) notify(taskID int32, status apigen.TaskStatus) {
	l.mu.Lock()
	watchers := l.watchers[taskID]
	delete(l.watchers, taskID)
	l.mu.Unlock()

	for sub := range watchers {
		sub.finish(TaskTerminalEvent{TaskID: taskID, Status: status}, true)
	}
}

func (l *PollingTaskEventListener) failSnapshot(snapshot map[int32][]*subscription, err error) {
	for taskID, subs := range snapshot {
		for _, sub := range subs {
			l.removeSubscription(taskID, sub, TaskTerminalEvent{TaskID: taskID, Err: err}, true)
		}
	}
}

func (l *PollingTaskEventListener) failAll(err error) {
	l.mu.Lock()
	watchers := l.watchers
	l.watchers = map[int32]map[*subscription]struct{}{}
	l.mu.Unlock()

	for taskID, subs := range watchers {
		for sub := range subs {
			sub.finish(TaskTerminalEvent{TaskID: taskID, Err: err}, true)
		}
	}
}

func (l *PollingTaskEventListener) removeSubscription(taskID int32, sub *subscription, event TaskTerminalEvent, send bool) {
	l.mu.Lock()
	if watchers, ok := l.watchers[taskID]; ok {
		delete(watchers, sub)
		if len(watchers) == 0 {
			delete(l.watchers, taskID)
		}
	}
	l.mu.Unlock()
	sub.finish(event, send)
}

func (s *subscription) finish(event TaskTerminalEvent, send bool) {
	s.once.Do(func() {
		if send {
			s.ch <- event
		}
		close(s.ch)
		close(s.done)
	})
}
