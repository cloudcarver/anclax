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

func TestAsyncTaskLeaseSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		workerID := uuid.New()
		workerIDParam := uuid.NullUUID{UUID: workerID, Valid: true}

		workerLabels := []string{"billing"}
		workerLabelsJSON, err := json.Marshal(workerLabels)
		require.NoError(t, err)

		_, err = m.UpsertWorker(ctx, querier.UpsertWorkerParams{
			ID:     workerID,
			Labels: workerLabelsJSON,
		})
		require.NoError(t, err)

		_, err = m.UpdateWorkerHeartbeat(ctx, workerID)
		require.NoError(t, err)

		store := taskcore.NewTaskStore(m)

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

		_, err = m.ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{
			ID:         claimed.ID,
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  true,
			Labels:     workerLabels,
		})
		require.ErrorIs(t, err, pgx.ErrNoRows)

		_, err = m.ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{
			ID:         taskOtherID,
			WorkerID:   workerIDParam,
			LockExpiry: &lockExpiry,
			HasLabels:  true,
			Labels:     workerLabels,
		})
		require.ErrorIs(t, err, pgx.ErrNoRows)

		_, err = m.RefreshTaskLock(ctx, querier.RefreshTaskLockParams{
			ID:       taskMatchID,
			WorkerID: workerIDParam,
		})
		require.NoError(t, err)

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

func TestAsyncTaskWorkerLoopSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		store := taskcore.NewTaskStore(m)
		labels := []string{"worker"}
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

		waitForSignal(t, handler.started(), 2*time.Second, "worker did not start task")
		lockedAt := waitForTaskLock(t, ctx, m, taskID, 2*time.Second)
		waitForLockRefresh(t, ctx, m, taskID, lockedAt, 2*time.Second)

		handler.release()
		waitForSignal(t, handler.done(), 2*time.Second, "worker did not finish task")
		waitForTaskCompletion(t, ctx, m, taskID, 2*time.Second)
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
