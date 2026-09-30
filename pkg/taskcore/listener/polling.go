package listener

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pkg/errors"
)

const (
	defaultPollInterval = time.Second
	defaultQueryTimeout = 5 * time.Second
	maxPollBackoff      = 30 * time.Second
	defaultBatchSize    = 256
)

var log = logger.NewLogAgent("task-listener")

type PollingTaskEventListener struct {
	model    model.ModelInterface
	mu       sync.RWMutex
	watchers map[int32]map[*subscription]struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
}

type subscription struct {
	ch   chan TaskTerminalEvent
	done chan struct{}
	once sync.Once
}

func NewPollingTaskEventListener(model model.ModelInterface) *PollingTaskEventListener {
	ctx, cancel := context.WithCancel(context.Background())
	l := &PollingTaskEventListener{
		model: model, watchers: map[int32]map[*subscription]struct{}{},
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
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

// WaitTask only registers a subscription. All database observations, including
// missing tasks and tasks that are already terminal, arrive through the channel.
func (l *PollingTaskEventListener) WaitTask(ctx context.Context, taskID int32) (<-chan TaskTerminalEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sub := &subscription{ch: make(chan TaskTerminalEvent, 1), done: make(chan struct{})}
	l.mu.Lock()
	if l.ctx.Err() != nil {
		l.mu.Unlock()
		return nil, ErrListenerClosed
	}
	if l.watchers[taskID] == nil {
		l.watchers[taskID] = map[*subscription]struct{}{}
	}
	l.watchers[taskID][sub] = struct{}{}
	l.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			l.removeSubscription(taskID, sub, TaskTerminalEvent{}, false)
		case <-sub.done:
		}
	}()
	return sub.ch, nil
}

func (l *PollingTaskEventListener) loop() {
	defer close(l.done)
	defer l.failAll(ErrListenerClosed)
	delay := defaultPollInterval
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-timer.C:
			if err := l.poll(l.ctx); err != nil {
				if l.ctx.Err() != nil {
					return
				}
				delay = min(delay*2, maxPollBackoff)
				log.Warnf("task status query failed; retrying in %s: %s", delay, err)
			} else {
				delay = defaultPollInterval
			}
			timer.Reset(delay)
		}
	}
}

func (l *PollingTaskEventListener) poll(ctx context.Context) error {
	ids, snapshot := l.snapshot()
	var retryErr error
	for batch := range slices.Chunk(ids, defaultBatchSize) {
		if err := ctx.Err(); err != nil {
			return err
		}
		queryCtx, cancel := context.WithTimeout(ctx, defaultQueryTimeout)
		start := time.Now()
		rows, err := l.model.ListTaskWaitStatuses(queryCtx, batch)
		metrics.TaskListenerPollDurationSeconds.Observe(time.Since(start).Seconds())
		cancel()
		if err != nil {
			if retryableQueryError(err) {
				retryErr = err
			} else {
				for _, id := range batch {
					l.finishSnapshot(id, snapshot[id], TaskTerminalEvent{TaskID: id, Err: errors.Wrap(err, "list task wait statuses")})
				}
			}
			continue
		}
		statuses := make(map[int32]apigen.TaskStatus, len(rows))
		for _, row := range rows {
			statuses[row.ID] = apigen.TaskStatus(row.Status)
		}
		for _, id := range batch {
			status, found := statuses[id]
			if !found {
				l.finishSnapshot(id, snapshot[id], TaskTerminalEvent{TaskID: id, Err: ErrTaskNotFound})
			} else if IsTerminalStatus(status) {
				l.finishSnapshot(id, snapshot[id], TaskTerminalEvent{TaskID: id, Status: status})
			}
		}
	}
	return retryErr
}

// Transport failures and temporary server errors are retried. Permanent server
// errors such as invalid SQL or permissions are reported to the waiters.
func retryableQueryError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return true
	}
	if len(pgErr.Code) < 2 {
		return false
	}
	switch pgErr.Code[:2] {
	case "08", "40", "53", "57":
		return true
	default:
		return pgErr.Code == "55P03"
	}
}

func (l *PollingTaskEventListener) snapshot() ([]int32, map[int32][]*subscription) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	ids := make([]int32, 0, len(l.watchers))
	subs := make(map[int32][]*subscription, len(l.watchers))
	for id, watchers := range l.watchers {
		ids = append(ids, id)
		for sub := range watchers {
			subs[id] = append(subs[id], sub)
		}
	}
	slices.Sort(ids)
	return ids, subs
}

// New subscribers must not receive stale results from a query started before
// their registration, especially a missing-task observation.
func (l *PollingTaskEventListener) finishSnapshot(id int32, subs []*subscription, event TaskTerminalEvent) {
	for _, sub := range subs {
		l.removeSubscription(id, sub, event, true)
	}
}

func (l *PollingTaskEventListener) failAll(err error) {
	l.mu.Lock()
	watchers := l.watchers
	l.watchers = map[int32]map[*subscription]struct{}{}
	l.mu.Unlock()
	for id, subs := range watchers {
		for sub := range subs {
			sub.finish(TaskTerminalEvent{TaskID: id, Err: err}, true)
		}
	}
}

func (l *PollingTaskEventListener) removeSubscription(id int32, sub *subscription, event TaskTerminalEvent, send bool) {
	l.mu.Lock()
	if watchers := l.watchers[id]; watchers != nil {
		delete(watchers, sub)
		if len(watchers) == 0 {
			delete(l.watchers, id)
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
