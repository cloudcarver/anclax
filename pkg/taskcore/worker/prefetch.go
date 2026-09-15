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
		prepared, reason, err := runTaskPrefetchBatch(ctx, m, task, owner, lockTTL)
		if err != nil {
			return err
		}
		if prepared < 0 {
			return taskcore.ErrTaskLockLost
		}
		interval, err := pacing.interval(reason)
		if err != nil {
			return err
		}
		if interval == 0 {
			continue
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

func runTaskPrefetchBatch(ctx context.Context, m model.ModelInterface, task Task, owner uuid.UUID, lockTTL time.Duration) (prepared int32, reason string, resultErr error) {
	started := time.Now()
	defer func() { observeScheduler("prefetch", started, resultErr) }()
	resultErr = m.RunTransactionWithTx(ctx, func(_ core.Tx, txm model.ModelInterface) error {
		result, err := txm.PrefetchTaskSupply(ctx, querier.PrefetchTaskSupplyParams{
			TaskID: task.ID, WorkerID: owner, LeaseVersion: task.LeaseVersion,
			BatchSize: 256, ReadyTtlMs: 2000, LockTtlMs: lockTTL.Milliseconds(),
		})
		if err == nil {
			prepared, reason = result.Prepared, result.WaitReason
		}
		return err
	})
	return prepared, reason, resultErr
}
