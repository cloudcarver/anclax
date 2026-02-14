//go:build smoke
// +build smoke

package taskcore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/taskcore"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

const (
	smokeContainerName = "anclax-pg-smoke"
	smokePort          = "5499"
)

// Verifies core lease lifecycle: claim, relock protections, refresh, and completion unlock semantics.
func TestAsyncTaskLeaseSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		// Set up an alive worker with labels so claim queries can enforce label matching.
		workerID := uuid.New()
		workerIDParam := uuid.NullUUID{UUID: workerID, Valid: true}

		workerLabels := []string{"billing"}
		workerLabelsJSON, err := json.Marshal(workerLabels)
		require.NoError(t, err)

		_, err = m.UpsertWorker(ctx, querier.UpsertWorkerParams{
			ID:                   workerID,
			Labels:               workerLabelsJSON,
			AppliedConfigVersion: 0,
		})
		require.NoError(t, err)

		_, err = m.UpdateWorkerHeartbeat(ctx, workerID)
		require.NoError(t, err)

		store := taskcore.NewTaskStore(m)

		// Enqueue one task that should be claimable by this worker and one that should be filtered out.
		labelsMatch := []string{"billing"}
		labelsOther := []string{"ops"}

		taskMatchID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{Labels: &labelsMatch},
			Spec:       apigen.TaskSpec{Type: "smoke", Payload: json.RawMessage(`{}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		taskOtherID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{Labels: &labelsOther},
			Spec:       apigen.TaskSpec{Type: "smoke", Payload: json.RawMessage(`{}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		// Verify generic claim picks the label-matching task and records lock ownership.
		lockExpiry := time.Now().Add(-1 * time.Minute)
		claimed, err := m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  true,
			Labels:     workerLabels,
		})
		require.NoError(t, err)
		require.Equal(t, taskMatchID, claimed.ID)
		require.NotNil(t, claimed.LockedAt)
		require.True(t, claimed.WorkerID.Valid)

		// Verify the same task cannot be re-claimed while already locked by this worker.
		_, err = m.ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{
			ID:         claimed.ID,
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  true,
			Labels:     workerLabels,
		})
		require.ErrorIs(t, err, pgx.ErrNoRows)

		// Verify an explicit claim-by-id still rejects tasks outside the worker's labels.
		_, err = m.ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{
			ID:         taskOtherID,
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  true,
			Labels:     workerLabels,
		})
		require.ErrorIs(t, err, pgx.ErrNoRows)

		// Verify lease extension works for the current lock owner.
		_, err = m.RefreshTaskLock(ctx, querier.RefreshTaskLockParams{
			ID:       taskMatchID,
			WorkerID: workerIDParam,
		})
		require.NoError(t, err)

		// Complete through worker-scoped update and verify lock ownership is cleared on terminal state.
		_, err = m.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
			ID:       taskMatchID,
			Status:   string(apigen.Completed),
			WorkerID: workerIDParam,
		})
		require.NoError(t, err)

		updated, err := m.GetTaskByID(ctx, taskMatchID)
		require.NoError(t, err)
		require.Equal(t, string(apigen.Completed), updated.Status)
		require.Nil(t, updated.LockedAt)
		require.False(t, updated.WorkerID.Valid)
	})
}

// Verifies the worker loop claims a task, refreshes the lock while running, and completes successfully.
func TestAsyncTaskWorkerLoopSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		store := taskcore.NewTaskStore(m)
		labels := []string{"worker"}
		// Enqueue a task that only a worker with the configured label should process.
		taskID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{Labels: &labels},
			Spec:       apigen.TaskSpec{Type: "smoke-worker", Payload: json.RawMessage(`{}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		pollInterval := 20 * time.Millisecond
		lockRefresh := 20 * time.Millisecond
		lockTTL := 200 * time.Millisecond
		heartbeat := 20 * time.Millisecond

		// Use short intervals so the smoke test can quickly observe lock acquisition and refresh.
		cfg := &config.Config{
			Worker: config.Worker{
				PollInterval:        &pollInterval,
				LockRefreshInterval: &lockRefresh,
				LockTTL:             &lockTTL,
				HeartbeatInterval:   &heartbeat,
				Labels:              labels,
			},
		}

		gctx := globalctx.New()
		handler := newSmokeWorkerHandler()
		workerInstance, err := worker.NewWorker(gctx, cfg, m, handler)
		require.NoError(t, err)
		go workerInstance.Start()
		t.Cleanup(gctx.Cancel)

		// Confirm the task started, then verify the lock exists and is refreshed while handler is blocked.
		waitForSignal(t, handler.started(), 2*time.Second, "worker did not start task")
		lockedAt := waitForTaskLock(t, ctx, m, taskID, 2*time.Second)
		waitForLockRefresh(t, ctx, m, taskID, lockedAt, 2*time.Second)

		// Unblock handler and verify the worker drives task to completed with lock/owner cleanup.
		handler.release()
		waitForSignal(t, handler.done(), 2*time.Second, "worker did not finish task")
		waitForTaskCompletion(t, ctx, m, taskID, 2*time.Second)
	})
}

// Verifies serial-key ordering gates claimability across started_at, serial_id ordering, and failure progression.
func TestAsyncTaskSerialBehaviorSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		store := taskcore.NewTaskStore(m)
		workerID := uuid.New()
		workerIDParam := uuid.NullUUID{UUID: workerID, Valid: true}
		lockExpiry := time.Now().Add(-1 * time.Minute)

		// Scenario 1: a future started_at head blocks the entire serial lane, even with other pending tasks.
		serialKey := "serial-smoke"
		serialID := int32(1)
		future := time.Now().Add(10 * time.Minute)

		blockedID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{SerialKey: &serialKey, SerialID: &serialID},
			Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":1}`)},
			Status:     apigen.Pending,
			StartedAt:  &future,
		})
		require.NoError(t, err)

		laterID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{SerialKey: &serialKey},
			Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":2}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)
		require.NotEqual(t, blockedID, laterID)

		// Nothing should be claimable yet because the head of this serial key is not started.
		_, err = m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.ErrorIs(t, err, pgx.ErrNoRows)

		// Adding another same-serial-id task still stays blocked by the not-yet-startable head.
		earlyID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{SerialKey: &serialKey, SerialID: &serialID},
			Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":0}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)
		require.NotEqual(t, blockedID, earlyID)

		_, err = m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.ErrorIs(t, err, pgx.ErrNoRows)

		// Once head started_at is in the past, claims should proceed in serial order.
		past := time.Now().Add(-1 * time.Minute)
		require.NoError(t, m.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{
			ID:        blockedID,
			StartedAt: &past,
		}))

		claimed, err := m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.NoError(t, err)
		require.Equal(t, blockedID, claimed.ID)

		// Completing current head should unblock the next deterministic candidate in the same serial key.
		_, err = m.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
			ID:       claimed.ID,
			Status:   string(apigen.Completed),
			WorkerID: workerIDParam,
		})
		require.NoError(t, err)

		claimed, err = m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.NoError(t, err)
		require.Equal(t, earlyID, claimed.ID)

		// After serial-id tasks are done, the remaining no-serial-id task becomes claimable.
		_, err = m.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
			ID:       claimed.ID,
			Status:   string(apigen.Completed),
			WorkerID: workerIDParam,
		})
		require.NoError(t, err)

		claimed, err = m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.NoError(t, err)
		require.Equal(t, laterID, claimed.ID)

		_, err = m.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
			ID:       claimed.ID,
			Status:   string(apigen.Completed),
			WorkerID: workerIDParam,
		})
		require.NoError(t, err)

		// Scenario 2: with multiple explicit serial IDs, claim order should be ascending serial_id.
		serialKeyIDs := "serial-ids"
		serialIDOne := int32(1)
		serialIDTwo := int32(2)
		serialIDThree := int32(3)

		idTwo, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{SerialKey: &serialKeyIDs, SerialID: &serialIDTwo},
			Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":2}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		idOne, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{SerialKey: &serialKeyIDs, SerialID: &serialIDOne},
			Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":1}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		idThree, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{SerialKey: &serialKeyIDs, SerialID: &serialIDThree},
			Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":3}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		claimed, err = m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.NoError(t, err)
		require.Equal(t, idOne, claimed.ID)

		_, err = m.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
			ID:       claimed.ID,
			Status:   string(apigen.Completed),
			WorkerID: workerIDParam,
		})
		require.NoError(t, err)

		claimed, err = m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.NoError(t, err)
		require.Equal(t, idTwo, claimed.ID)

		_, err = m.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
			ID:       claimed.ID,
			Status:   string(apigen.Completed),
			WorkerID: workerIDParam,
		})
		require.NoError(t, err)

		claimed, err = m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.NoError(t, err)
		require.Equal(t, idThree, claimed.ID)

		_, err = m.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
			ID:       claimed.ID,
			Status:   string(apigen.Completed),
			WorkerID: workerIDParam,
		})
		require.NoError(t, err)

		// Scenario 3: without serial IDs, queue still respects head started_at gating for the key.
		serialKeyNoID := "serial-noid"
		futureHead := time.Now().Add(5 * time.Minute)

		futureHeadID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{SerialKey: &serialKeyNoID},
			Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":10}`)},
			Status:     apigen.Pending,
			StartedAt:  &futureHead,
		})
		require.NoError(t, err)

		secondID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{SerialKey: &serialKeyNoID},
			Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":11}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		_, err = m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.ErrorIs(t, err, pgx.ErrNoRows)

		// Moving the head into the past should allow head-first claim, then the follower task.
		pastHead := time.Now().Add(-1 * time.Minute)
		require.NoError(t, m.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{
			ID:        futureHeadID,
			StartedAt: &pastHead,
		}))

		claimed, err = m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.NoError(t, err)
		require.Equal(t, futureHeadID, claimed.ID)

		_, err = m.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
			ID:       claimed.ID,
			Status:   string(apigen.Completed),
			WorkerID: workerIDParam,
		})
		require.NoError(t, err)

		claimed, err = m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.NoError(t, err)
		require.Equal(t, secondID, claimed.ID)

		_, err = m.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
			ID:       claimed.ID,
			Status:   string(apigen.Completed),
			WorkerID: workerIDParam,
		})
		require.NoError(t, err)

		// Scenario 4: a failed head should still release serial progression to the next task.
		serialKeyFail := "serial-fail"
		failIDOne := int32(1)
		failIDTwo := int32(2)

		failedID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{SerialKey: &serialKeyFail, SerialID: &failIDOne},
			Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":20}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		nextID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{SerialKey: &serialKeyFail, SerialID: &failIDTwo},
			Spec:       apigen.TaskSpec{Type: "smoke-serial", Payload: json.RawMessage(`{"id":21}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		claimed, err = m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.NoError(t, err)
		require.Equal(t, failedID, claimed.ID)

		_, err = m.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
			ID:       claimed.ID,
			Status:   string(apigen.Failed),
			WorkerID: workerIDParam,
		})
		require.NoError(t, err)

		claimed, err = m.ClaimTask(ctx, querier.ClaimTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.NoError(t, err)
		require.Equal(t, nextID, claimed.ID)
	})
}

// Verifies strict priority claim order and normal-task group claiming for weighted/default groups.
func TestAsyncTaskPriorityAndWeightClaimSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		// Set up worker identity for claim ownership; labels are irrelevant for this unfiltered claim path.
		workerID := uuid.New()
		workerIDParam := uuid.NullUUID{UUID: workerID, Valid: true}
		lockExpiry := time.Now().Add(-1 * time.Minute)

		emptyLabels, err := json.Marshal([]string{})
		require.NoError(t, err)
		_, err = m.UpsertWorker(ctx, querier.UpsertWorkerParams{
			ID:                   workerID,
			Labels:               emptyLabels,
			AppliedConfigVersion: 0,
		})
		require.NoError(t, err)

		store := taskcore.NewTaskStore(m)

		// Seed strict-priority tasks and normal tasks mapped to weighted groups plus default fallback group.
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
		require.NoError(t, err)

		strictHighID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{Priority: &pStrictHigh},
			Spec:       apigen.TaskSpec{Type: "smoke-priority", Payload: json.RawMessage(`{"n":2}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		normalDefaultID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{},
			Spec:       apigen.TaskSpec{Type: "smoke-priority", Payload: json.RawMessage(`{"n":3}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		normalW1ID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{Labels: &labelsW1, Weight: &wLow},
			Spec:       apigen.TaskSpec{Type: "smoke-priority", Payload: json.RawMessage(`{"n":4}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		normalW2ID, err := store.PushTask(ctx, &apigen.Task{
			Attributes: apigen.TaskAttributes{Labels: &labelsW2, Weight: &wHigh},
			Spec:       apigen.TaskSpec{Type: "smoke-priority", Payload: json.RawMessage(`{"n":5}`)},
			Status:     apigen.Pending,
		})
		require.NoError(t, err)

		// Strict lane should always claim higher priority before lower priority.
		claimed, err := m.ClaimStrictTask(ctx, querier.ClaimStrictTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.NoError(t, err)
		require.Equal(t, strictHighID, claimed.ID)

		claimed, err = m.ClaimStrictTask(ctx, querier.ClaimStrictTaskParams{
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  false,
			Labels:     nil,
		})
		require.NoError(t, err)
		require.Equal(t, strictLowID, claimed.ID)

		// Normal lane should return the task mapped to the requested weighted group.
		claimed, err = m.ClaimNormalTaskByGroup(ctx, querier.ClaimNormalTaskByGroupParams{
			WorkerID:       workerIDParam,
			LockExpiry:     &lockExpiry,
			HasLabels:      false,
			Labels:         nil,
			GroupName:      "w2",
			WeightedLabels: []string{"w1", "w2"},
		})
		require.NoError(t, err)
		require.Equal(t, normalW2ID, claimed.ID)

		// Verify other weighted group lookup is isolated to that group's tasks.
		claimed, err = m.ClaimNormalTaskByGroup(ctx, querier.ClaimNormalTaskByGroupParams{
			WorkerID:       workerIDParam,
			LockExpiry:     &lockExpiry,
			HasLabels:      false,
			Labels:         nil,
			GroupName:      "w1",
			WeightedLabels: []string{"w1", "w2"},
		})
		require.NoError(t, err)
		require.Equal(t, normalW1ID, claimed.ID)

		// Verify unlabeled/unknown-label normal tasks are claimable through the default group.
		claimed, err = m.ClaimNormalTaskByGroup(ctx, querier.ClaimNormalTaskByGroupParams{
			WorkerID:       workerIDParam,
			LockExpiry:     &lockExpiry,
			HasLabels:      false,
			Labels:         nil,
			GroupName:      "__default__",
			WeightedLabels: []string{"w1", "w2"},
		})
		require.NoError(t, err)
		require.Equal(t, normalDefaultID, claimed.ID)
	})
}

func withSmokePostgres(t *testing.T, fn func(ctx context.Context, m model.ModelInterface)) {
	t.Helper()
	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	cleanupContainer(t)
	if err := runDocker(t, "run", "-d", "--name", smokeContainerName,
		"-e", "POSTGRES_PASSWORD=postgres",
		"-e", "POSTGRES_USER=postgres",
		"-e", "POSTGRES_DB=postgres",
		"-p", smokePort+":5432",
		"postgres:15",
	); err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}
	t.Cleanup(func() { cleanupContainer(t) })

	dsn := "postgres://postgres:postgres@localhost:" + smokePort + "/postgres?sslmode=disable"
	if err := waitForPostgres(t, dsn, 15*time.Second); err != nil {
		t.Fatalf("postgres not ready: %v", err)
	}

	cfg := &config.Config{Pg: config.Pg{DSN: &dsn}}
	libCfg := config.DefaultLibConfig()
	cm := closer.NewCloserManager()
	t.Cleanup(cm.Close)

	m, err := model.NewModel(cfg, libCfg, cm)
	require.NoError(t, err)

	fn(context.Background(), m)
}

func dockerAvailable() bool {
	cmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	return cmd.Run() == nil
}

func runDocker(t *testing.T, args ...string) error {
	t.Helper()
	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %v failed: %w: %s", args, err, string(output))
	}
	return nil
}

func cleanupContainer(t *testing.T) {
	t.Helper()
	cmd := exec.Command("docker", "rm", "-f", smokeContainerName)
	_ = cmd.Run()
}

func waitForPostgres(t *testing.T, dsn string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			_ = conn.Close(ctx)
			cancel()
			return nil
		}
		cancel()
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for postgres")
}

func waitForSignal(t *testing.T, ch <-chan struct{}, timeout time.Duration, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatalf("%s", message)
	}
}

func waitForTaskLock(t *testing.T, ctx context.Context, m model.ModelInterface, taskID int32, timeout time.Duration) time.Time {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		task, err := m.GetTaskByID(ctx, taskID)
		if err == nil && task.LockedAt != nil && task.WorkerID.Valid {
			return *task.LockedAt
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task was not locked in time")
	return time.Time{}
}

func waitForLockRefresh(t *testing.T, ctx context.Context, m model.ModelInterface, taskID int32, initial time.Time, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		task, err := m.GetTaskByID(ctx, taskID)
		if err == nil && task.LockedAt != nil && task.LockedAt.After(initial) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task lock was not refreshed")
}

func waitForTaskCompletion(t *testing.T, ctx context.Context, m model.ModelInterface, taskID int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		task, err := m.GetTaskByID(ctx, taskID)
		if err == nil && task.Status == string(apigen.Completed) && task.LockedAt == nil && !task.WorkerID.Valid {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task did not complete")
}

type smokeWorkerHandler struct {
	startedCh chan struct{}
	proceedCh chan struct{}
	doneCh    chan struct{}
}

func newSmokeWorkerHandler() *smokeWorkerHandler {
	return &smokeWorkerHandler{
		startedCh: make(chan struct{}),
		proceedCh: make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

func (h *smokeWorkerHandler) HandleTask(ctx context.Context, spec worker.TaskSpec) error {
	if spec.GetType() != "smoke-worker" {
		return worker.ErrUnknownTaskType
	}
	signalOnce(h.startedCh)
	select {
	case <-h.proceedCh:
	case <-ctx.Done():
		return ctx.Err()
	}
	signalOnce(h.doneCh)
	return nil
}

func (h *smokeWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *smokeWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

func (h *smokeWorkerHandler) release() {
	signalOnce(h.proceedCh)
}

func (h *smokeWorkerHandler) started() <-chan struct{} {
	return h.startedCh
}

func (h *smokeWorkerHandler) done() <-chan struct{} {
	return h.doneCh
}

func signalOnce(ch chan struct{}) {
	select {
	case <-ch:
		return
	default:
		close(ch)
	}
}
