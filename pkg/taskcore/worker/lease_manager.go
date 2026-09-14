package worker

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/cloudcarver/anclax/pkg/metrics"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
)

const leaseRenewalBatchSize = 256

type leaseQueries interface {
	RefreshTaskLocks(context.Context, querier.RefreshTaskLocksParams) ([]*querier.RefreshTaskLocksRow, error)
	ListTaskWaitStatuses(context.Context, []int32) ([]*querier.ListTaskWaitStatusesRow, error)
}

type leaseRenewal struct {
	task     Task
	ctx      context.Context
	deadline time.Time
	nextDue  time.Time
	batch    *leaseBatch
}

type leaseBatch struct {
	entries []*leaseRenewal
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
}

// leaseManager owns all renewal scheduling for one worker. Database operations
// are bounded batches; the scheduling loop stays available to enforce deadlines
// even while every renewal connection is blocked.
type leaseManager struct {
	queries                leaseQueries
	workerID               uuid.NullUUID
	ttl, interval, quantum time.Duration
	concurrency            int
	interrupt              func(Task, error)

	mu        sync.Mutex
	entries   map[executionKey]*leaseRenewal
	active    int
	running   bool
	wake      chan struct{}
	nextSweep time.Time
	dueBefore time.Time
}

func newLeaseManager(q leaseQueries, workerID uuid.NullUUID, ttl, interval time.Duration, concurrency int, interrupt func(Task, error)) *leaseManager {
	return &leaseManager{
		queries: q, workerID: workerID, ttl: ttl, interval: interval,
		quantum:     max(time.Nanosecond, min(interval/4, (ttl-interval)/2, 250*time.Millisecond)),
		concurrency: max(1, concurrency), interrupt: interrupt,
		entries: make(map[executionKey]*leaseRenewal), wake: make(chan struct{}, 1),
	}
}

func (m *leaseManager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *leaseManager) register(ctx context.Context, task Task) func() {
	confirmed := task.claimedAt
	if confirmed.IsZero() {
		confirmed = time.Now()
	}
	e := &leaseRenewal{task: task, ctx: ctx, deadline: confirmed.Add(m.ttl), nextDue: confirmed.Add(m.interval)}
	m.mu.Lock()
	if _, exists := m.entries[task.executionKey()]; exists {
		m.mu.Unlock()
		panic("duplicate task lease renewal")
	}
	m.entries[task.executionKey()] = e
	if !m.running {
		m.running = true
		go m.loop()
	}
	m.mu.Unlock()
	if time.Until(confirmed.Add(m.ttl)) < m.quantum {
		m.signal()
	}

	return func() {
		m.mu.Lock()
		if m.entries[task.executionKey()] == e {
			delete(m.entries, task.executionKey())
		}
		batch := e.batch
		if batch != nil {
			m.cancelUnusedBatch(batch)
		}
		m.mu.Unlock()
		m.signal()
		// Finalization must not overlap a renewal that already included this
		// attempt. Other entries in that batch continue to renew independently.
		if batch != nil {
			<-batch.done
		}
	}
}

func (m *leaseManager) loop() {
	timer := time.NewTimer(m.quantum)
	defer timer.Stop()
	for {
		batches, expired, delay, idle := m.prepare()
		for _, e := range expired {
			metrics.TaskLeaseRenewalLostTotal.Inc()
			m.interrupt(e.task, taskcore.ErrTaskLockLost)
		}
		if idle {
			return
		}
		for _, batch := range batches {
			go m.renew(batch)
		}
		timer.Reset(delay)
		select {
		case <-m.wake:
		case <-timer.C:
		}
	}
}

func (m *leaseManager) prepare() (batches []*leaseBatch, expired []*leaseRenewal, delay time.Duration, idle bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if !now.Before(m.nextSweep) {
		m.dueBefore = now
		m.nextSweep = now.Add(m.quantum)
	}
	delay = m.nextSweep.Sub(now)
	var due []*leaseRenewal
	removedBatches := make(map[*leaseBatch]bool)
	for key, e := range m.entries {
		if e.ctx.Err() != nil {
			delete(m.entries, key)
			removedBatches[e.batch] = true
			continue
		}
		if !now.Before(e.deadline) {
			delete(m.entries, key)
			removedBatches[e.batch] = true
			expired = append(expired, e)
			continue
		}
		delay = min(delay, e.deadline.Sub(now))
		// A completed query only drains work already due in this sweep. Newly
		// due tasks join the next window instead of causing tiny query cascades.
		if e.batch == nil && !m.dueBefore.Before(e.nextDue) {
			due = append(due, e)
		}
	}
	for batch := range removedBatches {
		if batch != nil {
			m.cancelUnusedBatch(batch)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].deadline.Equal(due[j].deadline) {
			return due[i].task.ID < due[j].task.ID
		}
		return due[i].deadline.Before(due[j].deadline)
	})
	for len(due) > 0 && m.active < m.concurrency {
		n := min(len(due), leaseRenewalBatchSize)
		batch := &leaseBatch{entries: due[:n:n], done: make(chan struct{})}
		batch.ctx, batch.cancel = context.WithDeadline(context.Background(), batch.entries[0].deadline)
		for _, e := range batch.entries {
			e.batch = batch
		}
		batches = append(batches, batch)
		m.active++
		due = due[n:]
	}
	if len(m.entries) == 0 && m.active == 0 {
		m.running = false
		idle = true
	}
	return
}

// Called with mu held. One finishing task cannot cancel renewals for the other
// tasks in its batch, but an entirely abandoned batch should release its connection.
func (m *leaseManager) cancelUnusedBatch(batch *leaseBatch) {
	for _, e := range batch.entries {
		if m.entries[e.task.executionKey()] == e && e.ctx.Err() == nil {
			return
		}
	}
	batch.cancel()
}

func (m *leaseManager) renew(batch *leaseBatch) {
	defer batch.cancel()
	defer func() {
		m.mu.Lock()
		for _, e := range batch.entries {
			e.batch = nil
		}
		m.active--
		close(batch.done)
		m.mu.Unlock()
		m.signal()
	}()
	started := time.Now()
	params := querier.RefreshTaskLocksParams{WorkerID: m.workerID}
	for _, e := range batch.entries {
		params.Ids = append(params.Ids, e.task.ID)
		params.LeaseVersions = append(params.LeaseVersions, e.task.LeaseVersion)
	}
	ctx := batch.ctx
	rows, err := m.queries.RefreshTaskLocks(ctx, params)
	metrics.TaskLeaseRenewalDurationSeconds.Observe(time.Since(started).Seconds())
	metrics.TaskLeaseRenewalBatchSize.Observe(float64(len(params.Ids)))
	metrics.TaskLeaseRenewalBatchesTotal.Inc()
	if err != nil {
		metrics.TaskLeaseRenewalErrorsTotal.Inc()
		m.mu.Lock()
		for _, e := range batch.entries {
			e.nextDue = time.Now().Add(m.interval)
		}
		m.mu.Unlock()
		return
	}
	renewed := make(map[executionKey]bool, len(rows))
	for _, row := range rows {
		renewed[executionKey{row.ID, row.LeaseVersion}] = true
	}
	var missing []*leaseRenewal
	var ids []int32
	m.mu.Lock()
	now := time.Now()
	for _, e := range batch.entries {
		if m.entries[e.task.executionKey()] != e || e.ctx.Err() != nil {
			continue
		}
		if renewed[e.task.executionKey()] && now.Before(e.deadline) {
			// Use the request start rather than response time, so network and
			// connection-pool delays cannot extend the locally trusted lease.
			e.deadline = started.Add(m.ttl)
			e.nextDue = started.Add(m.interval)
		} else {
			missing = append(missing, e)
			ids = append(ids, e.task.ID)
		}
	}
	m.mu.Unlock()
	if len(missing) == 0 {
		return
	}
	// Control-plane status lookups use the same isolated renewal store.
	statuses, _ := m.queries.ListTaskWaitStatuses(ctx, ids)
	causes := make(map[int32]error, len(statuses))
	for _, row := range statuses {
		switch apigen.TaskStatus(row.Status) {
		case apigen.Paused:
			causes[row.ID] = taskcore.ErrTaskPaused
		case apigen.Cancelled:
			causes[row.ID] = taskcore.ErrTaskCancelled
		}
	}
	for _, e := range missing {
		m.mu.Lock()
		current := m.entries[e.task.executionKey()] == e
		if current {
			delete(m.entries, e.task.executionKey())
		}
		m.mu.Unlock()
		if current && e.ctx.Err() == nil {
			cause := causes[e.task.ID]
			if cause == nil {
				cause = taskcore.ErrTaskLockLost
			}
			metrics.TaskLeaseRenewalLostTotal.Inc()
			m.interrupt(e.task, cause)
		}
	}
}
