package worker

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/jackc/pgx/v5/pgconn"
)

// A rolled-back outcome transaction can be retried without executing the
// handler again. Connection/commit-ack errors have uncertain outcomes and are
// deliberately excluded. The worker slot is held throughout this operation.
func (p *ModelPort) finalizeWithRetry(ctx context.Context, task Task, execErr error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for attempt := 0; ; attempt++ {
		resume, deadline, err := p.pauseFinalizationLease(ctx, task)
		if err != nil {
			return err
		}
		txCtx := ctx
		txCancel := func() {}
		if !deadline.IsZero() {
			txCtx, txCancel = context.WithDeadline(ctx, deadline)
		}
		err = p.model.RunTransactionWithTx(txCtx, func(tx core.Tx, _ model.ModelInterface) error {
			return p.lifeCycleHandler.FinalizeAttempt(txCtx, tx, task, execErr)
		})
		txCancel()
		if err == nil {
			// Keep scheduling suspended until completeTaskRuntime drains it.
			return nil
		}
		resume()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != "40P01" && pgErr.Code != "40001" && pgErr.Code != "55P03") {
			return err
		}
		metrics.TaskFinalizeRetriesTotal.WithLabelValues(pgErr.Code).Inc()
		backoff := min(200*time.Millisecond, 5*time.Millisecond<<min(attempt, 6))
		timer := time.NewTimer(backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)+1)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
}

func (p *ModelPort) pauseFinalizationLease(ctx context.Context, task Task) (func(), time.Time, error) {
	resume := func() {}
	if p.leases == nil {
		return resume, time.Time{}, nil
	}
	m := p.leases
	m.mu.Lock()
	e := m.entries[task.executionKey()]
	if e == nil {
		m.mu.Unlock()
		return resume, time.Time{}, nil
	}
	e.paused = true
	batch := e.batch
	m.mu.Unlock()
	resume = func() {
		m.mu.Lock()
		e.paused = false
		m.mu.Unlock()
		m.signal()
	}
	// Renewal and finalization never update the same attempt concurrently.
	// A failed outcome resumes from the last confirmed deadline, not a fresh TTL.
	if batch != nil {
		select {
		case <-ctx.Done():
			resume()
			return nil, time.Time{}, ctx.Err()
		case <-batch.done:
		}
	}
	m.mu.Lock()
	deadline := e.deadline
	m.mu.Unlock()
	return resume, deadline, nil
}
