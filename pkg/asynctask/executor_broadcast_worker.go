package asynctask

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"time"

	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/taskcore/pgnotify"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	taskworker "github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
)

func (e *Executor) ExecuteBroadcastUpdateWorkerRuntimeConfig(ctx context.Context, task taskworker.Task, params *taskgen.BroadcastUpdateWorkerRuntimeConfigParameters) error {
	if params == nil {
		return errors.Wrap(taskcore.ErrFatalTask, "broadcast update worker runtime config params cannot be nil")
	}
	startAt := e.now()
	requestID := optionalRequestID(params.RequestID)
	if requestID == "" {
		requestID = uuid.NewString()
	}

	ackPollInterval, err := parseAckPollInterval(params.AckPollInterval)
	if err != nil {
		return errors.Wrap(taskcore.ErrFatalTask, err.Error())
	}

	labelWeights, err := buildBroadcastLabelWeights(params)
	if err != nil {
		return errors.Wrap(taskcore.ErrFatalTask, err.Error())
	}
	maxStrictPercentage, err := normalizeMaxStrictPercentage(params.MaxStrictPercentage)
	if err != nil {
		return errors.Wrap(taskcore.ErrFatalTask, err.Error())
	}

	payloadRaw, err := buildRuntimeConfigPayload(maxStrictPercentage, labelWeights)
	if err != nil {
		return err
	}
	created, err := e.model.CreateWorkerRuntimeConfig(ctx, payloadRaw)
	if err != nil {
		return errors.Wrap(err, "create worker runtime config")
	}
	targetVersion := created.Version
	localWorkerID := e.localWorkerID()
	targetWorkers, err := e.snapshotOrListAliveWorkers(ctx, params.WorkerIDs)
	if err != nil {
		return err
	}
	for _, workerID := range targetWorkers {
		if localWorkerID != "" && workerID.String() == localWorkerID && e.localWorker != nil {
			e.localWorker.NotifyRuntimeConfig(requestID)
			continue
		}
		if err := e.enqueueApplyRuntimeConfigToWorker(ctx, task.ID, requestID, workerID, targetVersion); err != nil {
			return err
		}
	}

	heartbeatTTL := e.workerHeartbeatTTL()
	targetSet := make(map[uuid.UUID]struct{}, len(targetWorkers))
	for _, workerID := range targetWorkers {
		targetSet[workerID] = struct{}{}
	}
	for {
		latest, err := e.model.GetLatestWorkerRuntimeConfig(ctx)
		if err != nil {
			return errors.Wrap(err, "get latest runtime config")
		}
		if latest.Version > targetVersion {
			metrics.RuntimeConfigSupersededTotal.Inc()
			return nil
		}
		laggingWorkers, err := e.model.ListLaggingAliveWorkers(ctx, querier.ListLaggingAliveWorkersParams{
			HeartbeatCutoff: e.now().Add(-heartbeatTTL),
			Version:         targetVersion,
		})
		if err != nil {
			return errors.Wrap(err, "list lagging alive workers")
		}
		aliveWorkers, err := e.listAliveWorkers(ctx)
		if err != nil {
			return err
		}
		aliveSet := make(map[uuid.UUID]struct{}, len(aliveWorkers))
		for _, workerID := range aliveWorkers {
			aliveSet[workerID] = struct{}{}
		}
		for _, workerID := range targetWorkers {
			if _, alive := aliveSet[workerID]; alive {
				continue
			}
			if err := e.cancelObsoleteWorkerCommandTask(ctx, applyRuntimeConfigUniqueTag(requestID, workerID, targetVersion)); err != nil {
				return err
			}
		}
		remaining := 0
		for _, workerID := range laggingWorkers {
			if _, ok := targetSet[workerID]; ok {
				remaining++
			}
		}
		metrics.RuntimeConfigLaggingWorkers.Set(float64(remaining))
		if remaining == 0 {
			metrics.RuntimeConfigConvergenceSeconds.Observe(e.now().Sub(startAt).Seconds())
			return nil
		}
		if err := sleepOrDone(ctx, ackPollInterval); err != nil {
			return err
		}
	}
}

func (e *Executor) ExecuteApplyWorkerRuntimeConfigToWorker(ctx context.Context, _ taskworker.Task, params *taskgen.ApplyWorkerRuntimeConfigToWorkerParameters) error {
	return errors.Wrap(taskcore.ErrFatalTask, "applyWorkerRuntimeConfigToWorker must be handled by worker control task handler")
}

func (e *Executor) ExecuteBroadcastCancelTask(ctx context.Context, task taskworker.Task, params *taskgen.BroadcastCancelTaskParameters) error {
	if params == nil {
		return errors.Wrap(taskcore.ErrFatalTask, "broadcast cancel task params cannot be nil")
	}
	taskIDs, err := normalizeBroadcastTaskIDs(params.TaskIDs)
	if err != nil {
		return errors.Wrap(taskcore.ErrFatalTask, err.Error())
	}
	requestID := optionalRequestID(params.RequestID)
	if requestID == "" {
		requestID = uuid.NewString()
	}
	ackPollInterval, err := parseAckPollInterval(params.AckPollInterval)
	if err != nil {
		return errors.Wrap(taskcore.ErrFatalTask, err.Error())
	}

	targetWorkers, err := e.aliveSubsetOfSnapshot(ctx, params.WorkerIDs)
	if err != nil {
		return err
	}
	if len(targetWorkers) == 0 {
		return nil
	}

	localWorkerID := e.localWorkerID()
	waitTargets := make([]uuid.UUID, 0, len(targetWorkers))
	for _, workerID := range targetWorkers {
		if localWorkerID != "" && workerID.String() == localWorkerID && e.localWorker != nil {
			e.localWorker.InterruptTasks(taskIDs, taskcore.ErrTaskCancelled)
			continue
		}
		if err := e.enqueueCancelTaskOnWorker(ctx, task.ID, requestID, workerID, taskIDs); err != nil {
			return err
		}
		waitTargets = append(waitTargets, workerID)
	}
	if len(waitTargets) == 0 {
		return nil
	}

	return e.waitForWorkerCommandTasks(ctx, waitTargets, ackPollInterval, func(workerID uuid.UUID) string {
		return cancelOnWorkerUniqueTag(requestID, workerID)
	})
}

func (e *Executor) ExecuteCancelTaskOnWorker(ctx context.Context, _ taskworker.Task, params *taskgen.CancelTaskOnWorkerParameters) error {
	return errors.Wrap(taskcore.ErrFatalTask, "cancelTaskOnWorker must be handled by worker control task handler")
}

func (e *Executor) ExecuteBroadcastPauseTask(ctx context.Context, task taskworker.Task, params *taskgen.BroadcastPauseTaskParameters) error {
	if params == nil {
		return errors.Wrap(taskcore.ErrFatalTask, "broadcast pause task params cannot be nil")
	}
	taskIDs, err := normalizeBroadcastTaskIDs(params.TaskIDs)
	if err != nil {
		return errors.Wrap(taskcore.ErrFatalTask, err.Error())
	}
	requestID := optionalRequestID(params.RequestID)
	if requestID == "" {
		requestID = uuid.NewString()
	}
	ackPollInterval, err := parseAckPollInterval(params.AckPollInterval)
	if err != nil {
		return errors.Wrap(taskcore.ErrFatalTask, err.Error())
	}

	targetWorkers, err := e.aliveSubsetOfSnapshot(ctx, params.WorkerIDs)
	if err != nil {
		return err
	}
	if len(targetWorkers) == 0 {
		return nil
	}

	localWorkerID := e.localWorkerID()
	waitTargets := make([]uuid.UUID, 0, len(targetWorkers))
	for _, workerID := range targetWorkers {
		if localWorkerID != "" && workerID.String() == localWorkerID && e.localWorker != nil {
			e.localWorker.InterruptTasks(taskIDs, taskcore.ErrTaskPaused)
			continue
		}
		if err := e.enqueuePauseTaskOnWorker(ctx, task.ID, requestID, workerID, taskIDs); err != nil {
			return err
		}
		waitTargets = append(waitTargets, workerID)
	}
	if len(waitTargets) == 0 {
		return nil
	}

	return e.waitForWorkerCommandTasks(ctx, waitTargets, ackPollInterval, func(workerID uuid.UUID) string {
		return pauseOnWorkerUniqueTag(requestID, workerID)
	})
}

func (e *Executor) ExecutePauseTaskOnWorker(ctx context.Context, _ taskworker.Task, params *taskgen.PauseTaskOnWorkerParameters) error {
	return errors.Wrap(taskcore.ErrFatalTask, "pauseTaskOnWorker must be handled by worker control task handler")
}

func buildBroadcastLabelWeights(params *taskgen.BroadcastUpdateWorkerRuntimeConfigParameters) (map[string]int32, error) {
	if params == nil {
		return nil, errors.New("broadcast update worker runtime config params cannot be nil")
	}
	return buildLabelWeights(&taskgen.UpdateWorkerRuntimeConfigParameters{
		DefaultWeight: params.DefaultWeight,
		Labels:        append([]string(nil), params.Labels...),
		Weights:       append([]int32(nil), params.Weights...),
	})
}

func buildRuntimeConfigPayload(maxStrictPercentage *int32, labelWeights map[string]int32) ([]byte, error) {
	payloadRaw, err := json.Marshal(pgnotify.RuntimeConfigPayload{
		MaxStrictPercentage: maxStrictPercentage,
		LabelWeights:        labelWeights,
	})
	if err != nil {
		return nil, errors.Wrap(err, "marshal runtime config payload")
	}
	return payloadRaw, nil
}

func (e *Executor) workerHeartbeatTTL() time.Duration {
	heartbeatTTL := e.runtimeConfigHeartbeatTTL
	if heartbeatTTL <= 0 {
		heartbeatTTL = defaultWorkerHeartbeatInterval * runtimeConfigHeartbeatTTLMultiplier
	}
	return heartbeatTTL
}

func (e *Executor) enqueueApplyRuntimeConfigToWorker(ctx context.Context, parentTaskID int32, requestID string, workerID uuid.UUID, version int64) error {
	if e.runner == nil {
		return errors.New("task runner is required for broadcast runtime config")
	}
	params := &taskgen.ApplyWorkerRuntimeConfigToWorkerParameters{
		WorkerID: workerID,
		Version:  version,
	}
	if requestID != "" {
		params.RequestID = &requestID
	}
	overrides := e.broadcastChildTaskOverrides(parentTaskID,
		taskcore.WithLabels([]string{workerLabel(workerID)}),
		taskcore.WithUniqueTag(applyRuntimeConfigUniqueTag(requestID, workerID, version)),
	)
	_, err := e.runner.RunApplyWorkerRuntimeConfigToWorker(ctx, params, overrides...)
	if err != nil {
		return errors.Wrapf(err, "enqueue apply runtime config task for worker %s", workerID)
	}
	return nil
}

func (e *Executor) enqueueCancelTaskOnWorker(ctx context.Context, parentTaskID int32, requestID string, workerID uuid.UUID, taskIDs []int32) error {
	if e.runner == nil {
		return errors.New("task runner is required for broadcast cancel task")
	}
	params := &taskgen.CancelTaskOnWorkerParameters{
		WorkerID: workerID,
		TaskIDs:  append([]int32(nil), taskIDs...),
	}
	if requestID != "" {
		params.RequestID = &requestID
	}
	overrides := e.broadcastChildTaskOverrides(parentTaskID,
		taskcore.WithLabels([]string{workerLabel(workerID)}),
		taskcore.WithUniqueTag(cancelOnWorkerUniqueTag(requestID, workerID)),
	)
	_, err := e.runner.RunCancelTaskOnWorker(ctx, params, overrides...)
	if err != nil {
		return errors.Wrapf(err, "enqueue cancel task command for worker %s", workerID)
	}
	return nil
}

func (e *Executor) enqueuePauseTaskOnWorker(ctx context.Context, parentTaskID int32, requestID string, workerID uuid.UUID, taskIDs []int32) error {
	if e.runner == nil {
		return errors.New("task runner is required for broadcast pause task")
	}
	params := &taskgen.PauseTaskOnWorkerParameters{
		WorkerID: workerID,
		TaskIDs:  append([]int32(nil), taskIDs...),
	}
	if requestID != "" {
		params.RequestID = &requestID
	}
	overrides := e.broadcastChildTaskOverrides(parentTaskID,
		taskcore.WithLabels([]string{workerLabel(workerID)}),
		taskcore.WithUniqueTag(pauseOnWorkerUniqueTag(requestID, workerID)),
	)
	_, err := e.runner.RunPauseTaskOnWorker(ctx, params, overrides...)
	if err != nil {
		return errors.Wrapf(err, "enqueue pause task command for worker %s", workerID)
	}
	return nil
}

func (e *Executor) listAliveWorkers(ctx context.Context) ([]uuid.UUID, error) {
	workers, err := e.model.ListOnlineWorkerIDs(ctx, e.now().Add(-e.workerHeartbeatTTL()))
	if err != nil {
		return nil, errors.Wrap(err, "list online workers")
	}
	return workers, nil
}

func normalizeWorkerIDSnapshot(workerIDs []uuid.UUID) []uuid.UUID {
	if len(workerIDs) == 0 {
		return nil
	}
	out := make([]uuid.UUID, 0, len(workerIDs))
	seen := make(map[uuid.UUID]struct{}, len(workerIDs))
	for _, workerID := range workerIDs {
		if workerID == uuid.Nil {
			continue
		}
		if _, ok := seen[workerID]; ok {
			continue
		}
		seen[workerID] = struct{}{}
		out = append(out, workerID)
	}
	return out
}

func (e *Executor) snapshotOrListAliveWorkers(ctx context.Context, workerIDs []uuid.UUID) ([]uuid.UUID, error) {
	workerIDs = normalizeWorkerIDSnapshot(workerIDs)
	if len(workerIDs) > 0 {
		return workerIDs, nil
	}
	return e.listAliveWorkers(ctx)
}

func (e *Executor) aliveSubsetOfSnapshot(ctx context.Context, workerIDs []uuid.UUID) ([]uuid.UUID, error) {
	snapshotIDs := normalizeWorkerIDSnapshot(workerIDs)
	if len(snapshotIDs) == 0 {
		return e.listAliveWorkers(ctx)
	}
	aliveWorkers, err := e.listAliveWorkers(ctx)
	if err != nil {
		return nil, err
	}
	aliveSet := make(map[uuid.UUID]struct{}, len(aliveWorkers))
	for _, workerID := range aliveWorkers {
		aliveSet[workerID] = struct{}{}
	}
	out := make([]uuid.UUID, 0, len(snapshotIDs))
	for _, workerID := range snapshotIDs {
		if _, ok := aliveSet[workerID]; ok {
			out = append(out, workerID)
		}
	}
	return out, nil
}

func (e *Executor) cancelObsoleteWorkerCommandTask(ctx context.Context, uniqueTag string) error {
	task, err := e.model.GetTaskByUniqueTag(ctx, &uniqueTag)
	if err != nil {
		if stdErrors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return errors.Wrapf(err, "get worker command task by unique tag %s", uniqueTag)
	}

	if apigen.TaskStatus(task.Status) != apigen.Pending {
		return nil
	}
	if err := e.model.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{ID: task.ID, Status: string(apigen.Cancelled)}); err != nil {
		return errors.Wrapf(err, "cancel pending worker command task %s", uniqueTag)
	}
	return nil
}

func (e *Executor) waitForWorkerCommandTasks(ctx context.Context, targetWorkers []uuid.UUID, fanoutInterval time.Duration, uniqueTagFn func(workerID uuid.UUID) string) error {
	targets := make(map[uuid.UUID]struct{}, len(targetWorkers))
	for _, workerID := range targetWorkers {
		targets[workerID] = struct{}{}
	}
	for {
		aliveWorkers, err := e.listAliveWorkers(ctx)
		if err != nil {
			return err
		}
		aliveSet := make(map[uuid.UUID]struct{}, len(aliveWorkers))
		for _, workerID := range aliveWorkers {
			aliveSet[workerID] = struct{}{}
		}

		allAckedOrDead := true
		for workerID := range targets {
			uniqueTag := uniqueTagFn(workerID)
			if _, alive := aliveSet[workerID]; !alive {
				if err := e.cancelObsoleteWorkerCommandTask(ctx, uniqueTag); err != nil {
					return err
				}
				continue
			}
			task, err := e.model.GetTaskByUniqueTag(ctx, &uniqueTag)
			if err != nil {
				if stdErrors.Is(err, pgx.ErrNoRows) {
					allAckedOrDead = false
					continue
				}
				return errors.Wrapf(err, "get worker command task by unique tag %s", uniqueTag)
			}
			switch apigen.TaskStatus(task.Status) {
			case apigen.Completed:
				continue
			case apigen.Failed:
				return errors.Errorf("worker command task failed (worker=%s task_id=%d unique_tag=%s)", workerID, task.ID, uniqueTag)
			default:
				allAckedOrDead = false
			}
		}
		if allAckedOrDead {
			return nil
		}
		if err := sleepOrDone(ctx, fanoutInterval); err != nil {
			return err
		}
	}
}

func normalizeBroadcastTaskIDs(taskIDs []int32) ([]int32, error) {
	if len(taskIDs) == 0 {
		return nil, errors.New("taskIDs must be non-empty")
	}
	out := make([]int32, 0, len(taskIDs))
	seen := make(map[int32]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if taskID <= 0 {
			return nil, errors.New("taskIDs must be positive")
		}
		if _, ok := seen[taskID]; ok {
			continue
		}
		seen[taskID] = struct{}{}
		out = append(out, taskID)
	}
	if len(out) == 0 {
		return nil, errors.New("taskIDs must be non-empty")
	}
	return out, nil
}

func parseAckPollInterval(raw *string) (time.Duration, error) {
	interval := time.Second
	if raw == nil || *raw == "" {
		return interval, nil
	}
	parsed, err := time.ParseDuration(*raw)
	if err != nil {
		return 0, errors.Wrap(err, "invalid ackPollInterval duration")
	}
	if parsed <= 0 {
		return 0, errors.New("ackPollInterval must be positive")
	}
	return parsed, nil
}

func (e *Executor) broadcastChildTaskOverrides(parentTaskID int32, overrides ...taskcore.TaskOverride) []taskcore.TaskOverride {
	out := append([]taskcore.TaskOverride{}, overrides...)
	// Keep internal worker-command fanout tasks claimable even when workers are
	// configured with maxStrictPercentage=0.
	out = append(out, taskcore.WithPriority(0))
	if parentTaskID > 0 {
		out = append(out, taskcore.WithParentTaskID(parentTaskID))
	}
	return out
}

func optionalRequestID(requestID *string) string {
	if requestID == nil {
		return ""
	}
	return *requestID
}

func workerLabel(workerID uuid.UUID) string {
	return fmt.Sprintf("worker:%s", workerID)
}

func applyRuntimeConfigUniqueTag(requestID string, workerID uuid.UUID, version int64) string {
	return fmt.Sprintf("broadcast:update_runtime_config:%s:%d:%s", requestID, version, workerID)
}

func cancelOnWorkerUniqueTag(requestID string, workerID uuid.UUID) string {
	return fmt.Sprintf("broadcast:cancel_task:%s:%s", requestID, workerID)
}

func pauseOnWorkerUniqueTag(requestID string, workerID uuid.UUID) string {
	return fmt.Sprintf("broadcast:pause_task:%s:%s", requestID, workerID)
}

func sleepOrDone(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
