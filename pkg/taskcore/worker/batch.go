package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
)

const DefaultClaimBatchSize = 32

func (e *Engine) claimBatch() []Command {
	if e.batch != nil {
		return nil
	}
	slots := min(e.claimBatchSize, e.concurrency-e.inFlight)
	strict := min(slots, max(0, e.strictCap-e.strictInFlight))
	groups, labels := e.nextNormalClaimGroups()
	e.nextCycleID++
	cmd := Command{Type: CmdClaimBatch, CycleID: e.nextCycleID, BatchSize: slots,
		StrictSlots: strict, Groups: groups, WeightedLabels: labels}
	e.batch = &cmd
	e.inFlight += slots
	e.strictInFlight += strict
	return []Command{cmd}
}

func (e *Engine) onClaimBatchResult(event Event) []Command {
	if e.batch == nil || e.batch.CycleID != event.CycleID {
		return nil
	}
	batch := *e.batch
	e.batch = nil
	e.inFlight -= batch.BatchSize
	e.strictInFlight -= batch.StrictSlots
	var commands []Command
	for _, task := range event.Tasks {
		e.nextCycleID++
		lane := LaneNormal
		if task.Priority > 0 {
			lane = LaneStrict
			e.strictInFlight++
		}
		cycle := &cycleState{ID: e.nextCycleID, Lane: lane, Phase: PhaseExecuting, Task: copyTask(task)}
		e.cycles[cycle.ID] = cycle
		e.inFlight++
		commands = append(commands, Command{Type: CmdExecuteTask, CycleID: cycle.ID, Task: copyTask(task)})
	}
	commands = append(commands, e.admitRequests()...)
	// A short/empty result must not spin. A full batch can immediately fill
	// remaining capacity; stopped workers still account/finalize returned leases.
	if event.Err == nil && len(event.Tasks) == batch.BatchSize {
		commands = append(commands, e.onPollTick()...)
	}
	return commands
}

func (p *ModelPort) ClaimBatch(ctx context.Context, req ClaimBatchRequest) (result []*Task, resultErr error) {
	started := time.Now()
	defer func() {
		observeScheduler("claim_batch", started, resultErr)
		if resultErr == nil {
			metrics.TaskClaimBatchSize.Observe(float64(len(result)))
		}
	}()
	if req.BatchSize < 1 || req.BatchSize > 256 || req.StrictSlots < 0 || req.StrictSlots > req.BatchSize {
		return nil, fmt.Errorf("invalid claim batch: size=%d strict=%d", req.BatchSize, req.StrictSlots)
	}
	claimedAt := time.Now()
	var out []*Task
	err := p.model.RunTransactionWithTx(ctx, func(_ core.Tx, txm model.ModelInterface) error {
		rows, err := txm.ClaimTaskBatch(ctx, querier.ClaimTaskBatchParams{WorkerID: p.workerIDParam,
			LockTtlMs: p.lockTTL.Milliseconds(), Labels: p.labels, BatchSize: int32(req.BatchSize),
			StrictSlots: int32(req.StrictSlots), GroupNames: req.Groups, WeightedLabels: req.WeightedLabels})
		if err != nil {
			return err
		}
		for _, row := range rows {
			task := taskFromQuerier(row)
			task.claimedAt = claimedAt
			out = append(out, task)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claim task batch: %w", err)
	}
	return out, nil
}
