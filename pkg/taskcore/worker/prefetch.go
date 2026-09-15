package worker

import (
	"context"
	"time"

	"github.com/cloudcarver/anclax/core"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
)

// PrefetchTaskType is reserved for the framework's durable admission/recovery job.
const PrefetchTaskType = "prefetchTasks"

func (p *ModelPort) executePrefetch(ctx context.Context, task Task) error {
	return RunTaskPrefetch(ctx, p.model, task, p.workerID, p.lockTTL)
}

// RunTaskPrefetch keeps one scheduler attempt alive across bounded batches.
// Waiting never holds a database transaction; the execution lease is renewed by
// the same keeper as other long-running tasks. Polling remains the idle mechanism.
func RunTaskPrefetch(ctx context.Context, m model.ModelInterface, task Task, owner uuid.UUID, lockTTL time.Duration) error {
	var pacing prefetchPacing
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		started := time.Now()
		consumption, err := m.ListWorkerPrefetchConsumption(ctx)
		observeScheduler("prefetch_consumption", started, err)
		if err != nil {
			return err
		}
		pacing.observe(started, consumption)
		if batch, probe := pacing.batch(started); batch > 0 {
			prepared, err := runTaskPrefetchBatch(ctx, m, task, owner, lockTTL, batch)
			if err != nil {
				return err
			}
			if prepared < 0 {
				return taskcore.ErrTaskLockLost
			}
			pacing.prepared(started, prepared, probe)
		}
		interval := pacing.interval()
		if untilProbe := pacing.lastRun.Add(prefetchProbeInterval).Sub(started); untilProbe > 0 {
			interval = min(interval, untilProbe)
		}
		timer := time.NewTimer(max(0, interval-time.Since(started)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func runTaskPrefetchBatch(ctx context.Context, m model.ModelInterface, task Task, owner uuid.UUID, lockTTL time.Duration, batch int32) (prepared int32, resultErr error) {
	started := time.Now()
	defer func() { observeScheduler("prefetch", started, resultErr) }()
	resultErr = m.RunTransactionWithTx(ctx, func(_ core.Tx, txm model.ModelInterface) error {
		var err error
		prepared, err = txm.PrefetchReadyTasks(ctx, querier.PrefetchReadyTasksParams{
			TaskID: task.ID, WorkerID: owner, LeaseVersion: task.LeaseVersion,
			BatchSize: batch, ReadyTtlMs: 2000, LockTtlMs: lockTTL.Milliseconds(),
		})
		return err
	})
	return prepared, resultErr
}
