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

// RunTaskPrefetch executes one fenced, bounded system scheduling round.
func RunTaskPrefetch(ctx context.Context, m model.ModelInterface, task Task, owner uuid.UUID, lockTTL time.Duration) (resultErr error) {
	started := time.Now()
	defer func() {
		if _, deferred := resultErr.(*taskcore.TaskDeferred); !deferred {
			observeScheduler("prefetch", started, resultErr)
		} else {
			observeScheduler("prefetch", started, nil)
		}
	}()
	var prepared int32
	err := m.RunTransactionWithTx(ctx, func(_ core.Tx, txm model.ModelInterface) error {
		var err error
		prepared, err = txm.PrefetchReadyTasks(ctx, querier.PrefetchReadyTasksParams{
			TaskID: task.ID, WorkerID: owner, LeaseVersion: task.LeaseVersion,
			BatchSize: 256, ReadyTtlMs: 2000, LockTtlMs: lockTTL.Milliseconds(),
		})
		return err
	})
	if err != nil {
		return err
	}
	// A finite batch yields the control lane to cancellation/configuration work.
	// Deferral reuses this durable task without events or accumulated attempts.
	if prepared > 0 {
		return taskcore.DeferTask(0)
	}
	return taskcore.DeferTask(20 * time.Millisecond)
}
