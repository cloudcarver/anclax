package worker

import (
	"context"
	"encoding/json"
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

// RunTaskPrefetch keeps one scheduler attempt alive. Admission can stop entirely
// while ready reserves are sufficient. Fenced observations and execution-lease
// recovery continue independently; the ordinary keeper renews the system task.
func RunTaskPrefetch(ctx context.Context, m model.ModelInterface, task Task, owner uuid.UUID, lockTTL time.Duration) error {
	var pacing prefetchPacing
	var nextProbe, nextAdmission, nextMaintenance time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := time.Now()
		if !now.Before(nextProbe) {
			maintain := !now.Before(nextMaintenance)
			rows, err := inspectTaskPrefetch(ctx, m, task, owner, lockTTL, maintain)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.GroupID < 0 {
					return taskcore.ErrTaskLockLost
				}
			}
			pacing.observe(now, rows)
			nextProbe = now.Add(pacing.probeInterval())
			if maintain {
				nextMaintenance = now.Add(prefetchMaintenance)
			}
			if nextMaintenance.Before(nextProbe) {
				nextProbe = nextMaintenance
			}
		}
		wanted, paused := pacing.admission()
		now = time.Now()
		if wanted && !now.Before(nextAdmission) {
			result, err := runTaskPrefetchBatch(ctx, m, task, owner, lockTTL, paused)
			if err != nil {
				return err
			}
			if result.Prepared < 0 {
				return taskcore.ErrTaskLockLost
			}
			var prepared map[int64]int64
			if err := json.Unmarshal(result.PreparedGroups, &prepared); err != nil {
				return err
			}
			pacing.prepared(prepared)
			interval, err := pacing.interval(result.WaitReason)
			if err != nil {
				return err
			}
			nextAdmission = now.Add(interval)
			continue
		}
		wake := nextProbe
		if wanted && nextAdmission.Before(wake) {
			wake = nextAdmission
		}
		timer := time.NewTimer(max(0, time.Until(wake)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func inspectTaskPrefetch(ctx context.Context, m model.ModelInterface, task Task, owner uuid.UUID, lockTTL time.Duration, maintain bool) (rows []*querier.InspectTaskPrefetchRow, resultErr error) {
	started := time.Now()
	defer func() { observeScheduler("prefetch_probe", started, resultErr) }()
	resultErr = m.RunTransactionWithTx(ctx, func(_ core.Tx, txm model.ModelInterface) error {
		var err error
		rows, err = txm.InspectTaskPrefetch(ctx, querier.InspectTaskPrefetchParams{
			TaskID: task.ID, WorkerID: owner, LeaseVersion: task.LeaseVersion,
			LockTtlMs: lockTTL.Milliseconds(), Maintain: maintain,
		})
		return err
	})
	return rows, resultErr
}

func runTaskPrefetchBatch(ctx context.Context, m model.ModelInterface, task Task, owner uuid.UUID, lockTTL time.Duration, paused []int64) (result *querier.PrefetchTaskSupplyRow, resultErr error) {
	started := time.Now()
	defer func() { observeScheduler("prefetch", started, resultErr) }()
	resultErr = m.RunTransactionWithTx(ctx, func(_ core.Tx, txm model.ModelInterface) error {
		var err error
		result, err = txm.PrefetchTaskSupply(ctx, querier.PrefetchTaskSupplyParams{
			TaskID: task.ID, WorkerID: owner, LeaseVersion: task.LeaseVersion,
			BatchSize: prefetchBatchSize, LockTtlMs: lockTTL.Milliseconds(), PausedGroups: paused,
		})
		return err
	})
	return result, resultErr
}
