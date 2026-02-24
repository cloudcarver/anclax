//go:build smoke
// +build smoke

package taskcore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/taskcore"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type smokeActor struct {
	model model.ModelInterface
}

func newSmokeActor(m model.ModelInterface) *smokeActor {
	return &smokeActor{model: m}
}

func (a *smokeActor) LeaseLifecycle(ctx context.Context) error {
	workerID := uuid.New()
	workerIDParam := uuid.NullUUID{UUID: workerID, Valid: true}

	workerLabels := []string{"billing"}
	workerLabelsJSON, err := json.Marshal(workerLabels)
	if err != nil {
		return err
	}

	if _, err := a.model.UpsertWorker(ctx, querier.UpsertWorkerParams{
		ID:                   workerID,
		Labels:               workerLabelsJSON,
		AppliedConfigVersion: 0,
	}); err != nil {
		return err
	}

	if _, err := a.model.UpdateWorkerHeartbeat(ctx, workerID); err != nil {
		return err
	}

	store := taskcore.NewTaskStore(a.model)
	labelsMatch := []string{"billing"}
	labelsOther := []string{"ops"}

	taskMatchID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{Labels: &labelsMatch},
		Spec:       apigen.TaskSpec{Type: "smoke", Payload: json.RawMessage(`{}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	taskOtherID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{Labels: &labelsOther},
		Spec:       apigen.TaskSpec{Type: "smoke", Payload: json.RawMessage(`{}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	lockExpiry := time.Now().Add(-1 * time.Minute)
	claimed, err := a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  true,
		Labels:     workerLabels,
	})
	if err != nil {
		return err
	}
	if claimed.ID != taskMatchID {
		return fmt.Errorf("expected task %d, got %d", taskMatchID, claimed.ID)
	}
	if claimed.LockedAt == nil || !claimed.WorkerID.Valid {
		return fmt.Errorf("expected locked task")
	}

	_, err = a.model.ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{
		ID:         claimed.ID,
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  true,
		Labels:     workerLabels,
	})
	if err := expectNoRows(err, "expected claim-by-id to fail on locked task"); err != nil {
		return err
	}

	_, err = a.model.ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{
		ID:         taskOtherID,
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  true,
		Labels:     workerLabels,
	})
	if err := expectNoRows(err, "expected claim-by-id to reject non-matching labels"); err != nil {
		return err
	}

	if _, err := a.model.RefreshTaskLock(ctx, querier.RefreshTaskLockParams{
		ID:       taskMatchID,
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       taskMatchID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	updated, err := a.model.GetTaskByID(ctx, taskMatchID)
	if err != nil {
		return err
	}
	if updated.Status != string(apigen.Completed) || updated.LockedAt != nil || updated.WorkerID.Valid {
		return fmt.Errorf("unexpected final state for task %d", taskMatchID)
	}
	if err := a.model.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskOtherID,
		Status: string(apigen.Completed),
	}); err != nil {
		return err
	}
	return nil
}

func (a *smokeActor) WorkerLoop(ctx context.Context) error {
	store := taskcore.NewTaskStore(a.model)
	labels := []string{"worker"}
	taskID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{Labels: &labels},
		Spec:       apigen.TaskSpec{Type: "smoke-worker", Payload: json.RawMessage(`{}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	pollInterval := 20 * time.Millisecond
	lockRefresh := 20 * time.Millisecond
	lockTTL := 200 * time.Millisecond
	heartbeat := 20 * time.Millisecond

	cfg := &config.Config{
		Worker: config.Worker{
			PollInterval:        &pollInterval,
			LockRefreshInterval: &lockRefresh,
			LockTTL:             &lockTTL,
			HeartbeatInterval:   &heartbeat,
			Labels:              labels,
		},
	}

	handler := newSmokeWorkerHandler()
	gctx := globalctx.New()
	workerInstance, err := worker.NewWorker(gctx, cfg, a.model, handler)
	if err != nil {
		return err
	}
	go workerInstance.Start()
	defer gctx.Cancel()

	if err := a.waitForSignal(handler.started(), 2*time.Second, "worker did not start task"); err != nil {
		return err
	}
	lockedAt, err := a.waitForTaskLock(ctx, taskID, 2*time.Second)
	if err != nil {
		return err
	}
	if err := a.waitForLockRefresh(ctx, taskID, lockedAt, 2*time.Second); err != nil {
		return err
	}

	handler.release()
	if err := a.waitForSignal(handler.done(), 2*time.Second, "worker did not finish task"); err != nil {
		return err
	}
	return a.waitForTaskCompletion(ctx, taskID, 2*time.Second)
}

func (a *smokeActor) SerialBehavior(ctx context.Context) error {
	store := taskcore.NewTaskStore(a.model)
	workerID := uuid.New()
	workerIDParam := uuid.NullUUID{UUID: workerID, Valid: true}
	lockExpiry := time.Now().Add(-1 * time.Minute)

	serialKey := "serial-smoke"
	serialID := int32(1)
	future := time.Now().Add(10 * time.Minute)

	blockedID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialKey: &serialKey, SerialID: &serialID},
		Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":1}`)},
		Status:     apigen.Pending,
		StartedAt:  &future,
	})
	if err != nil {
		return err
	}

	laterID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialKey: &serialKey},
		Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":2}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}
	if blockedID == laterID {
		return fmt.Errorf("expected distinct task IDs")
	}

	_, err = a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err := expectNoRows(err, "expected no claim while head is future"); err != nil {
		return err
	}

	earlyID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialKey: &serialKey, SerialID: &serialID},
		Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":0}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}
	if blockedID == earlyID {
		return fmt.Errorf("expected distinct task IDs")
	}

	_, err = a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err := expectNoRows(err, "expected no claim while head is future after duplicate"); err != nil {
		return err
	}

	past := time.Now().Add(-1 * time.Minute)
	if err := a.model.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{
		ID:        blockedID,
		StartedAt: &past,
	}); err != nil {
		return err
	}

	claimed, err := a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err != nil {
		return err
	}
	if claimed.ID != blockedID {
		return fmt.Errorf("expected blocked task claimed first")
	}

	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	claimed, err = a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err != nil {
		return err
	}
	if claimed.ID != earlyID {
		return fmt.Errorf("expected early serial-id task next")
	}

	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	claimed, err = a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err != nil {
		return err
	}
	if claimed.ID != laterID {
		return fmt.Errorf("expected non-serial task last")
	}

	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	serialKeyIDs := "serial-ids"
	serialIDOne := int32(1)
	serialIDTwo := int32(2)
	serialIDThree := int32(3)

	idTwo, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialKey: &serialKeyIDs, SerialID: &serialIDTwo},
		Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":2}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	idOne, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialKey: &serialKeyIDs, SerialID: &serialIDOne},
		Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":1}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	idThree, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialKey: &serialKeyIDs, SerialID: &serialIDThree},
		Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":3}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	claimed, err = a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err != nil {
		return err
	}
	if claimed.ID != idOne {
		return fmt.Errorf("expected serial id 1 first")
	}

	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	claimed, err = a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err != nil {
		return err
	}
	if claimed.ID != idTwo {
		return fmt.Errorf("expected serial id 2 second")
	}

	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	claimed, err = a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err != nil {
		return err
	}
	if claimed.ID != idThree {
		return fmt.Errorf("expected serial id 3 third")
	}

	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	serialKeyNoID := "serial-noid"
	futureHead := time.Now().Add(5 * time.Minute)

	futureHeadID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialKey: &serialKeyNoID},
		Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":10}`)},
		Status:     apigen.Pending,
		StartedAt:  &futureHead,
	})
	if err != nil {
		return err
	}

	secondID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialKey: &serialKeyNoID},
		Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":11}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	_, err = a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err := expectNoRows(err, "expected no claim while head is future for no-id serial"); err != nil {
		return err
	}

	pastHead := time.Now().Add(-1 * time.Minute)
	if err := a.model.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{
		ID:        futureHeadID,
		StartedAt: &pastHead,
	}); err != nil {
		return err
	}

	claimed, err = a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err != nil {
		return err
	}
	if claimed.ID != futureHeadID {
		return fmt.Errorf("expected future head claimed first")
	}

	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	claimed, err = a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err != nil {
		return err
	}
	if claimed.ID != secondID {
		return fmt.Errorf("expected second task after head")
	}

	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	serialKeyFail := "serial-fail"
	failIDOne := int32(1)
	failIDTwo := int32(2)

	failedID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialKey: &serialKeyFail, SerialID: &failIDOne},
		Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":20}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	nextID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialKey: &serialKeyFail, SerialID: &failIDTwo},
		Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":21}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	claimed, err = a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err != nil {
		return err
	}
	if claimed.ID != failedID {
		return fmt.Errorf("expected failed head first")
	}

	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Failed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	claimed, err = a.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err != nil {
		return err
	}
	if claimed.ID != nextID {
		return fmt.Errorf("expected next serial task after failure")
	}
	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}
	return nil
}

func (a *smokeActor) PriorityAndWeight(ctx context.Context) error {
	workerID := uuid.New()
	workerIDParam := uuid.NullUUID{UUID: workerID, Valid: true}
	lockExpiry := time.Now().Add(-1 * time.Minute)

	emptyLabels, err := json.Marshal([]string{})
	if err != nil {
		return err
	}
	if _, err := a.model.UpsertWorker(ctx, querier.UpsertWorkerParams{
		ID:                   workerID,
		Labels:               emptyLabels,
		AppliedConfigVersion: 0,
	}); err != nil {
		return err
	}

	store := taskcore.NewTaskStore(a.model)
	pStrictLow := int32(1)
	pStrictHigh := int32(9)
	wLow := int32(1)
	wHigh := int32(7)
	labelsW1 := []string{"w1"}
	labelsW2 := []string{"w2"}

	strictLowID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{Priority: &pStrictLow},
		Spec:       apigen.TaskSpec{Type: "smoke-priority", Payload: json.RawMessage(`{"n":1}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	strictHighID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{Priority: &pStrictHigh},
		Spec:       apigen.TaskSpec{Type: "smoke-priority", Payload: json.RawMessage(`{"n":2}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	normalDefaultID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{},
		Spec:       apigen.TaskSpec{Type: "smoke-priority", Payload: json.RawMessage(`{"n":3}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	normalW1ID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{Labels: &labelsW1, Weight: &wLow},
		Spec:       apigen.TaskSpec{Type: "smoke-priority", Payload: json.RawMessage(`{"n":4}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	normalW2ID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{Labels: &labelsW2, Weight: &wHigh},
		Spec:       apigen.TaskSpec{Type: "smoke-priority", Payload: json.RawMessage(`{"n":5}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	claimed, err := a.model.ClaimStrictTask(ctx, querier.ClaimStrictTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err != nil {
		return err
	}
	if claimed.ID != strictHighID {
		return fmt.Errorf("expected strict high priority first")
	}
	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	claimed, err = a.model.ClaimStrictTask(ctx, querier.ClaimStrictTaskParams{
		WorkerID:   workerIDParam,
		LockExpiry: &lockExpiry,
		HasLabels:  false,
		Labels:     nil,
	})
	if err != nil {
		return err
	}
	if claimed.ID != strictLowID {
		return fmt.Errorf("expected strict low priority second")
	}
	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	claimed, err = a.model.ClaimNormalTaskByGroup(ctx, querier.ClaimNormalTaskByGroupParams{
		WorkerID:       workerIDParam,
		LockExpiry:     &lockExpiry,
		HasLabels:      false,
		Labels:         nil,
		GroupName:      "w2",
		WeightedLabels: []string{"w1", "w2"},
	})
	if err != nil {
		return err
	}
	if claimed.ID != normalW2ID {
		return fmt.Errorf("expected weighted w2 task")
	}
	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	claimed, err = a.model.ClaimNormalTaskByGroup(ctx, querier.ClaimNormalTaskByGroupParams{
		WorkerID:       workerIDParam,
		LockExpiry:     &lockExpiry,
		HasLabels:      false,
		Labels:         nil,
		GroupName:      "w1",
		WeightedLabels: []string{"w1", "w2"},
	})
	if err != nil {
		return err
	}
	if claimed.ID != normalW1ID {
		return fmt.Errorf("expected weighted w1 task")
	}
	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}

	claimed, err = a.model.ClaimNormalTaskByGroup(ctx, querier.ClaimNormalTaskByGroupParams{
		WorkerID:       workerIDParam,
		LockExpiry:     &lockExpiry,
		HasLabels:      false,
		Labels:         nil,
		GroupName:      "__default__",
		WeightedLabels: []string{"w1", "w2"},
	})
	if err != nil {
		return err
	}
	if claimed.ID != normalDefaultID {
		return fmt.Errorf("expected default group task")
	}
	if _, err := a.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       claimed.ID,
		Status:   string(apigen.Completed),
		WorkerID: workerIDParam,
	}); err != nil {
		return err
	}
	return nil
}

func (a *smokeActor) RetryPolicy(ctx context.Context) error {
	store := taskcore.NewTaskStore(a.model)
	retryPolicy := &apigen.TaskRetryPolicy{Interval: "150ms", MaxAttempts: 2}

	taskID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{RetryPolicy: retryPolicy},
		Spec:       apigen.TaskSpec{Type: "smoke-retry", Payload: json.RawMessage(`{}`)},
		Status:     apigen.Pending,
	})
	if err != nil {
		return err
	}

	pollInterval := 20 * time.Millisecond
	lockRefresh := 20 * time.Millisecond
	lockTTL := 200 * time.Millisecond
	heartbeat := 20 * time.Millisecond

	cfg := &config.Config{
		Worker: config.Worker{
			PollInterval:        &pollInterval,
			LockRefreshInterval: &lockRefresh,
			LockTTL:             &lockTTL,
			HeartbeatInterval:   &heartbeat,
		},
	}

	handler := newRetryWorkerHandler()
	gctx := globalctx.New()
	workerInstance, err := worker.NewWorker(gctx, cfg, a.model, handler)
	if err != nil {
		return err
	}
	go workerInstance.Start()
	defer gctx.Cancel()

	if err := a.waitForSignal(handler.firstAttempt(), 2*time.Second, "retry worker did not start first attempt"); err != nil {
		return err
	}
	anchor := time.Now()
	startedAt, err := a.waitForTaskStartedAfter(ctx, taskID, anchor, 2*time.Second)
	if err != nil {
		return err
	}
	if !startedAt.After(anchor) {
		return fmt.Errorf("expected started_at updated after first retry")
	}

	task, err := a.model.GetTaskByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status != string(apigen.Pending) || task.LockedAt != nil {
		return fmt.Errorf("expected pending task after retry scheduling")
	}

	if err := a.waitForSignal(handler.secondAttempt(), 3*time.Second, "retry worker did not start second attempt"); err != nil {
		return err
	}
	return a.waitForTaskCompletion(ctx, taskID, 3*time.Second)
}

func (a *smokeActor) Cronjob(ctx context.Context) error {
	store := taskcore.NewTaskStore(a.model)
	cronExpression := "*/1 * * * * *"
	initialStart := time.Now().Add(-1 * time.Second)

	taskID, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{Cronjob: &apigen.TaskCronjob{CronExpression: cronExpression}},
		Spec:       apigen.TaskSpec{Type: "smoke-cron", Payload: json.RawMessage(`{}`)},
		Status:     apigen.Pending,
		StartedAt:  &initialStart,
	})
	if err != nil {
		return err
	}

	pollInterval := 20 * time.Millisecond
	lockRefresh := 20 * time.Millisecond
	lockTTL := 200 * time.Millisecond
	heartbeat := 20 * time.Millisecond

	cfg := &config.Config{
		Worker: config.Worker{
			PollInterval:        &pollInterval,
			LockRefreshInterval: &lockRefresh,
			LockTTL:             &lockTTL,
			HeartbeatInterval:   &heartbeat,
		},
	}

	handler := newCronWorkerHandler()
	gctx := globalctx.New()
	workerInstance, err := worker.NewWorker(gctx, cfg, a.model, handler)
	if err != nil {
		return err
	}
	go workerInstance.Start()
	defer gctx.Cancel()

	if err := a.waitForSignal(handler.ran(), 2*time.Second, "cron worker did not run"); err != nil {
		return err
	}
	startedAt, err := a.waitForTaskStartedAfter(ctx, taskID, initialStart, 2*time.Second)
	if err != nil {
		return err
	}
	if !startedAt.After(initialStart) {
		return fmt.Errorf("expected cronjob to update started_at")
	}
	if err := a.waitForTaskUnlock(ctx, taskID, 2*time.Second); err != nil {
		return err
	}

	task, err := a.model.GetTaskByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status != string(apigen.Pending) || task.LockedAt != nil {
		return fmt.Errorf("expected pending unlocked cron task")
	}
	if err := a.model.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Completed),
	}); err != nil {
		return err
	}
	return nil
}

func (a *smokeActor) RuntimeConfigPoll(ctx context.Context) error {
	workerID := uuid.New()
	workerIDStr := workerID.String()
	pollInterval := 20 * time.Millisecond
	heartbeat := 20 * time.Millisecond
	runtimePoll := 50 * time.Millisecond

	cfg := &config.Config{
		Worker: config.Worker{
			PollInterval:              &pollInterval,
			HeartbeatInterval:         &heartbeat,
			RuntimeConfigPollInterval: &runtimePoll,
			WorkerID:                  &workerIDStr,
		},
	}

	handler := &noopWorkerHandler{}
	gctx := globalctx.New()
	workerInstance, err := worker.NewWorker(gctx, cfg, a.model, handler)
	if err != nil {
		return err
	}
	go workerInstance.Start()
	defer gctx.Cancel()

	if _, err := a.waitForOnlineWorkers(ctx, 1, 2*time.Second); err != nil {
		return err
	}

	payload := runtimeConfigPayloadSpec{
		MaxStrictPercentage: int32Ptr(20),
		LabelWeights: map[string]int32{
			"default": 1,
			"w1":      3,
		},
	}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	created, err := a.model.CreateWorkerRuntimeConfig(ctx, payloadRaw)
	if err != nil {
		return err
	}

	notify := runtimeConfigNotifySpec{
		Op: "up_config",
		Params: runtimeConfigNotifyParams{
			RequestID: "req-1",
			Version:   created.Version,
		},
	}
	notifyRaw, err := json.Marshal(notify)
	if err != nil {
		return err
	}
	if err := a.model.NotifyWorkerRuntimeConfig(ctx, string(notifyRaw)); err != nil {
		return err
	}

	return a.waitForWorkerLaggingState(ctx, workerID, created.Version, false, 3*time.Second)
}

func (a *smokeActor) RuntimeConfigListenAck(ctx context.Context) error {
	dsn := smokePostgresDSN()
	workerID := uuid.New()
	workerIDStr := workerID.String()
	pollInterval := 20 * time.Millisecond
	heartbeat := 20 * time.Millisecond

	cfg := &config.Config{
		Pg: config.Pg{DSN: &dsn},
		Worker: config.Worker{
			PollInterval:      &pollInterval,
			HeartbeatInterval: &heartbeat,
			WorkerID:          &workerIDStr,
		},
	}

	handler := &noopWorkerHandler{}
	gctx := globalctx.New()
	workerInstance, err := worker.NewWorker(gctx, cfg, a.model, handler)
	if err != nil {
		return err
	}
	go workerInstance.Start()
	defer gctx.Cancel()

	if _, err := a.waitForOnlineWorkers(ctx, 1, 2*time.Second); err != nil {
		return err
	}

	payload := runtimeConfigPayloadSpec{MaxStrictPercentage: int32Ptr(15), LabelWeights: map[string]int32{"default": 1}}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	created, err := a.model.CreateWorkerRuntimeConfig(ctx, payloadRaw)
	if err != nil {
		return err
	}

	requestID := "req-ack-1"
	ackCh := make(chan runtimeConfigAckSpec, 1)
	errCh := make(chan error, 1)
	go func() {
		ack, err := a.waitForRuntimeConfigAck(ctx, dsn, requestID, 4*time.Second)
		if err != nil {
			errCh <- err
			return
		}
		ackCh <- ack
	}()

	notify := runtimeConfigNotifySpec{
		Op: "up_config",
		Params: runtimeConfigNotifyParams{
			RequestID: requestID,
			Version:   created.Version,
		},
	}
	notifyRaw, err := json.Marshal(notify)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if err := a.model.NotifyWorkerRuntimeConfig(ctx, string(notifyRaw)); err != nil {
			return err
		}
		select {
		case err := <-errCh:
			return err
		case ack := <-ackCh:
			if ack.Params.RequestID != requestID || ack.Params.AppliedVersion != created.Version {
				return fmt.Errorf("unexpected ack payload")
			}
			return a.waitForWorkerLaggingState(ctx, workerID, created.Version, false, 3*time.Second)
		case <-time.After(150 * time.Millisecond):
			continue
		}
	}
	select {
	case err := <-errCh:
		return err
	case ack := <-ackCh:
		if ack.Params.RequestID != requestID || ack.Params.AppliedVersion != created.Version {
			return fmt.Errorf("unexpected ack payload")
		}
		return a.waitForWorkerLaggingState(ctx, workerID, created.Version, false, 3*time.Second)
	default:
		return fmt.Errorf("timeout waiting for runtime config ack")
	}
}

func (a *smokeActor) WorkerOffline(ctx context.Context) error {
	dsn := smokePostgresDSN()
	pollInterval := 20 * time.Millisecond
	heartbeat := 1 * time.Hour
	workerID := uuid.New()
	workerIDStr := workerID.String()

	cfg := &config.Config{
		Pg: config.Pg{DSN: &dsn},
		Worker: config.Worker{
			PollInterval:      &pollInterval,
			HeartbeatInterval: &heartbeat,
			WorkerID:          &workerIDStr,
		},
	}

	handler := &noopWorkerHandler{}
	gctx := globalctx.New()
	workerInstance, err := worker.NewWorker(gctx, cfg, a.model, handler)
	if err != nil {
		return err
	}
	go workerInstance.Start()
	defer gctx.Cancel()

	if _, err := a.waitForOnlineWorkers(ctx, 1, 2*time.Second); err != nil {
		return err
	}

	payload := runtimeConfigPayloadSpec{MaxStrictPercentage: int32Ptr(5)}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	created, err := a.model.CreateWorkerRuntimeConfig(ctx, payloadRaw)
	if err != nil {
		return err
	}

	if err := a.model.MarkWorkerOffline(ctx, workerID); err != nil {
		return err
	}
	if err := a.waitForWorkerLaggingState(ctx, workerID, created.Version, false, 2*time.Second); err != nil {
		return err
	}
	if err := a.waitForWorkerOnlineState(ctx, workerID, false, 2*time.Second); err != nil {
		return err
	}
	return nil
}

func (a *smokeActor) FailureEvent(ctx context.Context) error {
	store := taskcore.NewTaskStore(a.model)
	payload, err := json.Marshal(map[string]string{"id": "fail"})
	if err != nil {
		return err
	}

	taskID, err := store.PushTask(ctx, &apigen.Task{
		Spec:   apigen.TaskSpec{Type: "smoke-fail", Payload: payload},
		Status: apigen.Pending,
	})
	if err != nil {
		return err
	}

	pollInterval := 20 * time.Millisecond
	lockRefresh := 20 * time.Millisecond
	lockTTL := 200 * time.Millisecond
	heartbeat := 20 * time.Millisecond

	cfg := &config.Config{
		Worker: config.Worker{
			PollInterval:        &pollInterval,
			LockRefreshInterval: &lockRefresh,
			LockTTL:             &lockTTL,
			HeartbeatInterval:   &heartbeat,
		},
	}

	failErr := errors.New("intentional failure")
	handler := newFailureWorkerHandler("smoke-fail", failErr)
	gctx := globalctx.New()
	workerInstance, err := worker.NewWorker(gctx, cfg, a.model, handler)
	if err != nil {
		return err
	}
	go workerInstance.Start()
	defer gctx.Cancel()

	if err := a.waitForSignal(handler.failed(), 2*time.Second, "failure hook not invoked"); err != nil {
		return err
	}
	if err := a.waitForTaskFailed(ctx, taskID, 2*time.Second); err != nil {
		return err
	}

	event, err := a.model.GetLastTaskErrorEvent(ctx, taskID)
	if err != nil {
		return err
	}
	if event.Spec.TaskError == nil || event.Spec.TaskError.Error != failErr.Error() {
		return fmt.Errorf("unexpected task error event")
	}
	return nil
}

func (a *smokeActor) HighContention(ctx context.Context) error {
	store := taskcore.NewTaskStore(a.model)
	tracker := newContentionTracker()
	taskCount := 40

	for i := 1; i <= taskCount; i++ {
		payload, err := json.Marshal(contentionPayload{ID: i})
		if err != nil {
			return err
		}
		if _, err := store.PushTask(ctx, &apigen.Task{
			Spec:   apigen.TaskSpec{Type: "smoke-contention", Payload: payload},
			Status: apigen.Pending,
		}); err != nil {
			return err
		}
	}

	pollInterval := 10 * time.Millisecond
	lockRefresh := 20 * time.Millisecond
	lockTTL := 200 * time.Millisecond
	heartbeat := 20 * time.Millisecond
	concurrency := 3

	workerCount := 3
	workers := make([]*globalctx.GlobalContext, 0, workerCount)
	for i := 0; i < workerCount; i++ {
		cfg := &config.Config{
			Worker: config.Worker{
				PollInterval:        &pollInterval,
				LockRefreshInterval: &lockRefresh,
				LockTTL:             &lockTTL,
				HeartbeatInterval:   &heartbeat,
				Concurrency:         &concurrency,
			},
		}
		handler := newContentionWorkerHandler("smoke-contention", tracker)
		gctx := globalctx.New()
		workerInstance, err := worker.NewWorker(gctx, cfg, a.model, handler)
		if err != nil {
			return err
		}
		go workerInstance.Start()
		workers = append(workers, gctx)
	}
	defer func() {
		for _, gctx := range workers {
			gctx.Cancel()
		}
	}()

	if err := a.waitForNoPendingTasks(ctx, 5*time.Second); err != nil {
		return err
	}
	return tracker.assertCounts(taskCount)
}

func (a *smokeActor) WorkerExitRecovery(ctx context.Context) error {
	store := taskcore.NewTaskStore(a.model)
	taskID, err := store.PushTask(ctx, &apigen.Task{
		Spec:   apigen.TaskSpec{Type: "smoke-exit", Payload: json.RawMessage(`{}`)},
		Status: apigen.Pending,
	})
	if err != nil {
		return err
	}

	pollInterval := 10 * time.Millisecond
	lockTTL := 150 * time.Millisecond
	lockRefresh := 20 * time.Millisecond
	heartbeat := 20 * time.Millisecond

	cfg1 := &config.Config{
		Worker: config.Worker{
			PollInterval:        &pollInterval,
			LockTTL:             &lockTTL,
			LockRefreshInterval: &lockRefresh,
			HeartbeatInterval:   &heartbeat,
		},
	}

	handler1 := newBlockingWorkerHandler("smoke-exit")
	gctx1 := globalctx.New()
	workerOne, err := worker.NewWorker(gctx1, cfg1, a.model, handler1)
	if err != nil {
		return err
	}
	go workerOne.Start()

	if err := a.waitForSignal(handler1.started(), 2*time.Second, "exit worker did not start"); err != nil {
		return err
	}
	gctx1.Cancel()

	time.Sleep(lockTTL + 50*time.Millisecond)

	cfg2 := &config.Config{
		Worker: config.Worker{
			PollInterval:        &pollInterval,
			LockTTL:             &lockTTL,
			LockRefreshInterval: &pollInterval,
			HeartbeatInterval:   &heartbeat,
		},
	}

	handler2 := newSignalWorkerHandler("smoke-exit")
	gctx2 := globalctx.New()
	workerTwo, err := worker.NewWorker(gctx2, cfg2, a.model, handler2)
	if err != nil {
		return err
	}
	go workerTwo.Start()
	defer gctx2.Cancel()

	if err := a.waitForSignal(handler2.done(), 2*time.Second, "recovery worker did not finish"); err != nil {
		return err
	}
	return a.waitForTaskCompletion(ctx, taskID, 2*time.Second)
}

func (a *smokeActor) LockLoss(ctx context.Context) error {
	store := taskcore.NewTaskStore(a.model)
	taskID, err := store.PushTask(ctx, &apigen.Task{
		Spec:   apigen.TaskSpec{Type: "smoke-lock-loss", Payload: json.RawMessage(`{}`)},
		Status: apigen.Pending,
	})
	if err != nil {
		return err
	}

	pollInterval := 20 * time.Millisecond
	lockTTL := 100 * time.Millisecond
	lockRefresh := time.Duration(0)
	heartbeat := 20 * time.Millisecond

	cfg1 := &config.Config{
		Worker: config.Worker{
			PollInterval:        &pollInterval,
			LockTTL:             &lockTTL,
			LockRefreshInterval: &lockRefresh,
			HeartbeatInterval:   &heartbeat,
		},
	}

	handler1 := newBlockingWorkerHandler("smoke-lock-loss")
	gctx1 := globalctx.New()
	workerOne, err := worker.NewWorker(gctx1, cfg1, a.model, handler1)
	if err != nil {
		return err
	}
	go workerOne.Start()
	defer gctx1.Cancel()

	if err := a.waitForSignal(handler1.started(), 2*time.Second, "lock-loss worker did not start"); err != nil {
		return err
	}
	time.Sleep(lockTTL + 50*time.Millisecond)

	cfg2 := &config.Config{
		Worker: config.Worker{
			PollInterval:        &pollInterval,
			LockTTL:             &lockTTL,
			LockRefreshInterval: &pollInterval,
			HeartbeatInterval:   &heartbeat,
		},
	}

	handler2 := newSignalWorkerHandler("smoke-lock-loss")
	gctx2 := globalctx.New()
	workerTwo, err := worker.NewWorker(gctx2, cfg2, a.model, handler2)
	if err != nil {
		return err
	}
	go workerTwo.Start()
	defer gctx2.Cancel()

	if err := a.waitForSignal(handler2.done(), 2*time.Second, "takeover worker did not finish"); err != nil {
		return err
	}
	if err := a.waitForTaskCompletion(ctx, taskID, 2*time.Second); err != nil {
		return err
	}

	handler1.release()
	if err := a.waitForSignal(handler1.done(), 2*time.Second, "lock-loss worker did not finish"); err != nil {
		return err
	}

	task, err := a.model.GetTaskByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status != string(apigen.Completed) || task.LockedAt != nil {
		return fmt.Errorf("unexpected final lock-loss status")
	}
	return nil
}

func (a *smokeActor) waitForSignal(ch <-chan struct{}, timeout time.Duration, message string) error {
	select {
	case <-ch:
		return nil
	case <-time.After(timeout):
		return errors.New(message)
	}
}

func (a *smokeActor) waitForTaskLock(ctx context.Context, taskID int32, timeout time.Duration) (time.Time, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		default:
		}
		task, err := a.model.GetTaskByID(ctx, taskID)
		if err == nil && task.LockedAt != nil && task.WorkerID.Valid {
			return *task.LockedAt, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return time.Time{}, fmt.Errorf("task was not locked in time")
}

func (a *smokeActor) waitForLockRefresh(ctx context.Context, taskID int32, initial time.Time, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		task, err := a.model.GetTaskByID(ctx, taskID)
		if err == nil && task.LockedAt != nil && task.LockedAt.After(initial) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("task lock was not refreshed")
}

func (a *smokeActor) waitForTaskCompletion(ctx context.Context, taskID int32, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		task, err := a.model.GetTaskByID(ctx, taskID)
		if err == nil && task.Status == string(apigen.Completed) && task.LockedAt == nil && !task.WorkerID.Valid {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("task did not complete")
}

func (a *smokeActor) waitForTaskFailed(ctx context.Context, taskID int32, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		task, err := a.model.GetTaskByID(ctx, taskID)
		if err == nil && task.Status == string(apigen.Failed) && task.LockedAt == nil && !task.WorkerID.Valid {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("task did not fail")
}

func (a *smokeActor) waitForTaskUnlock(ctx context.Context, taskID int32, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		task, err := a.model.GetTaskByID(ctx, taskID)
		if err == nil && task.LockedAt == nil && !task.WorkerID.Valid {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("task lock was not released")
}

func (a *smokeActor) waitForTaskStartedAfter(ctx context.Context, taskID int32, after time.Time, timeout time.Duration) (time.Time, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		default:
		}
		task, err := a.model.GetTaskByID(ctx, taskID)
		if err == nil && task.StartedAt != nil && task.StartedAt.After(after) {
			return *task.StartedAt, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return time.Time{}, fmt.Errorf("task started_at was not updated in time")
}

func (a *smokeActor) waitForOnlineWorkers(ctx context.Context, min int, timeout time.Duration) ([]uuid.UUID, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		cutoff := time.Now().Add(-1 * time.Minute)
		workers, err := a.model.ListOnlineWorkerIDs(ctx, cutoff)
		if err == nil && len(workers) >= min {
			return workers, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, fmt.Errorf("online workers did not appear in time")
}

func (a *smokeActor) waitForLaggingWorkersCount(ctx context.Context, version int64, expected int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		cutoff := time.Now().Add(-1 * time.Minute)
		workers, err := a.model.ListLaggingAliveWorkers(ctx, querier.ListLaggingAliveWorkersParams{
			HeartbeatCutoff: cutoff,
			Version:         version,
		})
		if err == nil && len(workers) == expected {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("lagging worker count did not reach %d", expected)
}

func (a *smokeActor) waitForWorkerLaggingState(ctx context.Context, workerID uuid.UUID, version int64, expected bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		cutoff := time.Now().Add(-1 * time.Minute)
		workers, err := a.model.ListLaggingAliveWorkers(ctx, querier.ListLaggingAliveWorkersParams{
			HeartbeatCutoff: cutoff,
			Version:         version,
		})
		if err == nil {
			found := containsUUID(workers, workerID)
			if found == expected {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("worker lagging state did not reach expected=%v", expected)
}

func (a *smokeActor) waitForWorkerOnlineState(ctx context.Context, workerID uuid.UUID, expected bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		cutoff := time.Now().Add(-1 * time.Minute)
		workers, err := a.model.ListOnlineWorkerIDs(ctx, cutoff)
		if err == nil {
			found := containsUUID(workers, workerID)
			if found == expected {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("worker online state did not reach expected=%v", expected)
}

func containsUUID(list []uuid.UUID, target uuid.UUID) bool {
	for _, id := range list {
		if id == target {
			return true
		}
	}
	return false
}

func (a *smokeActor) waitForNoPendingTasks(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		pending, err := a.model.ListAllPendingTasks(ctx)
		if err == nil && len(pending) == 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("pending tasks did not drain")
}

func (a *smokeActor) waitForRuntimeConfigAck(ctx context.Context, dsn string, requestID string, timeout time.Duration) (runtimeConfigAckSpec, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return runtimeConfigAckSpec{}, err
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", runtimeConfigAckChannel)); err != nil {
		return runtimeConfigAckSpec{}, err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		waitCtx, cancel := context.WithTimeout(ctx, time.Until(deadline))
		notification, err := conn.WaitForNotification(waitCtx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				break
			}
			continue
		}
		if notification == nil {
			continue
		}
		var ack runtimeConfigAckSpec
		if err := json.Unmarshal([]byte(notification.Payload), &ack); err != nil {
			continue
		}
		if ack.Op != "ack" || ack.Params.RequestID != requestID {
			continue
		}
		return ack, nil
	}
	return runtimeConfigAckSpec{}, fmt.Errorf("timeout waiting for runtime config ack")
}

func expectNoRows(err error, message string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err == nil {
		return errors.New(message)
	}
	return err
}

type contentionPayload struct {
	ID int `json:"id"`
}

type contentionTracker struct {
	mu   sync.Mutex
	seen map[int]int
}

func newContentionTracker() *contentionTracker {
	return &contentionTracker{seen: make(map[int]int)}
}

func (t *contentionTracker) record(id int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seen[id]++
}

func (t *contentionTracker) assertCounts(expected int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := 1; i <= expected; i++ {
		count := t.seen[i]
		if count != 1 {
			return fmt.Errorf("task %d processed %d times", i, count)
		}
	}
	return nil
}

type contentionWorkerHandler struct {
	taskType string
	tracker  *contentionTracker
}

func newContentionWorkerHandler(taskType string, tracker *contentionTracker) *contentionWorkerHandler {
	return &contentionWorkerHandler{taskType: taskType, tracker: tracker}
}

func (h *contentionWorkerHandler) HandleTask(ctx context.Context, spec worker.TaskSpec) error {
	if spec.GetType() != h.taskType {
		return worker.ErrUnknownTaskType
	}
	var payload contentionPayload
	if err := json.Unmarshal(spec.GetPayload(), &payload); err != nil {
		return err
	}
	h.tracker.record(payload.ID)
	return nil
}

func (h *contentionWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *contentionWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}
