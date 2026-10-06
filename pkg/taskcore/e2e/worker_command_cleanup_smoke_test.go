//go:build smoke

package taskcoree2e_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudcarver/anclax"
	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/asynctask"
	"github.com/cloudcarver/anclax/pkg/config"
	taskstore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestWorkerCommandCleanupSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		for _, command := range []string{"cancel", "pause"} {
			for _, childStatus := range []string{"pending", "running"} {
				t.Run(command+"/"+childStatus, func(t *testing.T) {
					require.NoError(t, resetDSTState(ctx, m))
					store := taskstore.NewTaskStore(m)
					runner := taskgen.NewTaskRunner(store)
					executor := asynctask.NewExecutor(&config.Config{}, m, runner)
					handler := taskgen.NewTaskHandler(executor)
					targetID := uuid.New()
					targetLabels := []string{"worker:" + targetID.String()}
					_, err := m.UpsertWorker(ctx, querier.UpsertWorkerParams{ID: targetID, Labels: []byte(`[]`)})
					require.NoError(t, err)
					originalID, err := store.PushTask(ctx, &apigen.Task{Status: apigen.Cancelled, Spec: apigen.TaskSpec{Type: "original", Payload: []byte(`{}`)}})
					require.NoError(t, err)
					var parentID int32
					if command == "cancel" {
						parentID, err = runner.RunBroadcastCancelTask(ctx, &taskgen.BroadcastCancelTaskParameters{TaskIDs: []int32{originalID}, WorkerIDs: []uuid.UUID{targetID}}, taskstore.WithPriority(0))
					} else {
						parentID, err = runner.RunBroadcastPauseTask(ctx, &taskgen.BroadcastPauseTaskParameters{TaskIDs: []int32{originalID}, WorkerIDs: []uuid.UUID{targetID}}, taskstore.WithPriority(0))
					}
					require.NoError(t, err)
					coordinatorID := uuid.New()
					port, err := worker.NewModelPort(m, coordinatorID, nil, handler, time.Minute, 0)
					require.NoError(t, err)
					parent, err := port.ClaimControl(ctx, worker.ClaimRequest{})
					require.NoError(t, err)
					require.Equal(t, parentID, parent.ID)
					execErr := port.ExecuteTask(ctx, *parent)
					var deferred *taskstore.TaskDeferred
					require.ErrorAs(t, execErr, &deferred)
					require.NoError(t, port.FinalizeTask(ctx, *parent, execErr))
					cleanupTag := fmt.Sprintf("broadcast:cleanup_worker_commands:%d", parentID)
					_, err = m.GetTaskByUniqueTag(ctx, &cleanupTag)
					require.ErrorIs(t, err, pgx.ErrNoRows, "deferral must not enqueue cleanup")
					childTag := fmt.Sprintf("broadcast:%s_task:task:%d:%s", command, parentID, targetID)
					child, err := m.GetTaskByUniqueTag(ctx, &childTag)
					require.NoError(t, err)
					if childStatus == "running" {
						targetPort, err := worker.NewModelPort(m, targetID, targetLabels, nil, time.Minute, 0)
						require.NoError(t, err)
						claimed, err := targetPort.ClaimControl(ctx, worker.ClaimRequest{})
						require.NoError(t, err)
						require.Equal(t, child.ID, claimed.ID)
					}
					// The target disappears between broadcast attempts. Its command is
					// still durable, but no surviving worker has its routing label.
					_, err = conn.Exec(ctx, "UPDATE anclax.workers SET last_heartbeat=statement_timestamp()-interval '1 minute' WHERE id=$1", targetID)
					require.NoError(t, err)
					require.NoError(t, m.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{ID: parentID}))
					parent, err = port.ClaimControl(ctx, worker.ClaimRequest{})
					require.NoError(t, err)
					require.Equal(t, parentID, parent.ID)
					require.NoError(t, port.ExecuteTask(ctx, *parent))
					// Handler success, even when repeated, must not publish cleanup.
					require.NoError(t, handler.HandleTask(ctx, *parent))
					_, err = m.GetTaskByUniqueTag(ctx, &cleanupTag)
					require.ErrorIs(t, err, pgx.ErrNoRows)
					cleanupPort, err := worker.NewModelPort(m, uuid.New(), nil, handler, time.Minute, 0)
					require.NoError(t, err)
					_, err = cleanupPort.ClaimControl(ctx, worker.ClaimRequest{})
					require.ErrorIs(t, err, worker.ErrNoTask)
					// Both writes exist inside finalization, but remain invisible to
					// another worker until commit. Rolling back must undo both.
					rollbackErr := errors.New("rollback finalization")
					lifecycle := worker.NewTaskLifeCycleHandler(m, handler, coordinatorID)
					err = m.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
						require.NoError(t, lifecycle.FinalizeAttempt(ctx, tx, *parent, nil))
						finished, err := txm.GetTaskByID(ctx, parent.ID)
						require.NoError(t, err)
						require.Equal(t, "completed", finished.Status)
						_, err = txm.GetTaskByUniqueTag(ctx, &cleanupTag)
						require.NoError(t, err)
						visible, err := m.GetTaskByID(ctx, parent.ID)
						require.NoError(t, err)
						require.Equal(t, "running", visible.Status)
						_, err = cleanupPort.ClaimControl(ctx, worker.ClaimRequest{})
						require.ErrorIs(t, err, worker.ErrNoTask, "uncommitted cleanup cannot be claimed")
						return rollbackErr
					})
					require.ErrorIs(t, err, rollbackErr)
					_, err = m.GetTaskByUniqueTag(ctx, &cleanupTag)
					require.ErrorIs(t, err, pgx.ErrNoRows)
					unchanged, err := m.GetTaskByID(ctx, child.ID)
					require.NoError(t, err)
					require.Equal(t, childStatus, unchanged.Status)
					require.NoError(t, port.FinalizeTask(ctx, *parent, nil))
					// A stale finalization cannot enqueue another cleanup.
					require.NoError(t, port.FinalizeTask(ctx, *parent, nil))
					var cleanupCount int
					require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE unique_tag=$1", cleanupTag).Scan(&cleanupCount))
					require.Equal(t, 1, cleanupCount)
					cleanup, err := cleanupPort.ClaimControl(ctx, worker.ClaimRequest{})
					require.NoError(t, err)
					require.Equal(t, taskgen.CleanupWorkerCommandTasks, cleanup.GetType())
					require.True(t, worker.IsControlTask(cleanup.GetType()))
					require.Nil(t, cleanup.Attributes.Labels)

					expected := map[int32]string{child.ID: "cancelled", originalID: "cancelled", parentID: "completed", cleanup.ID: "completed"}
					for _, fixture := range []struct {
						taskType, status, want string
						parentID               int32
					}{
						{"cancelTaskOnWorker", "completed", "completed", parentID},
						{"cancelTaskOnWorker", "failed", "failed", parentID},
						{"cancelTaskOnWorker", "cancelled", "cancelled", parentID},
						{"pauseTaskOnWorker", "paused", "cancelled", parentID},
						{"applyWorkerRuntimeConfigToWorker", "pending", "cancelled", parentID},
						{"business-child", "pending", "pending", parentID},
						{"cancelTaskOnWorker", "pending", "pending", originalID},
					} {
						id, err := store.PushTask(ctx, &apigen.Task{Status: apigen.TaskStatus(fixture.status), ParentTaskId: &fixture.parentID,
							Attributes: apigen.TaskAttributes{Labels: &targetLabels}, Spec: apigen.TaskSpec{Type: fixture.taskType, Payload: []byte(`{}`)}})
						require.NoError(t, err)
						expected[id] = fixture.want
					}
					require.NoError(t, cleanupPort.ExecuteTask(ctx, *cleanup))
					require.NoError(t, cleanupPort.FinalizeTask(ctx, *cleanup, nil))
					require.NoError(t, handler.HandleTask(ctx, *cleanup), "repeated cleanup is idempotent")
					for id, status := range expected {
						row, err := m.GetTaskByID(ctx, id)
						require.NoError(t, err, "cleanup preserves task history")
						require.Equal(t, status, row.Status, "task %d", id)
					}
				})
			}
		}
		t.Run("enqueue failure rolls back parent", func(t *testing.T) {
			require.NoError(t, resetDSTState(ctx, m))
			runner := taskgen.NewTaskRunner(taskstore.NewTaskStore(m))
			handler := taskgen.NewTaskHandler(asynctask.NewExecutor(&config.Config{}, m, runner))
			parentID, err := runner.RunBroadcastCancelTask(ctx, &taskgen.BroadcastCancelTaskParameters{TaskIDs: []int32{1}})
			require.NoError(t, err)
			port, err := worker.NewModelPort(m, uuid.New(), nil, handler, time.Minute, 0)
			require.NoError(t, err)
			parent, err := port.ClaimControl(ctx, worker.ClaimRequest{})
			require.NoError(t, err)
			require.Equal(t, parentID, parent.ID)
			require.NoError(t, port.ExecuteTask(ctx, *parent))
			_, err = conn.Exec(ctx, `
CREATE FUNCTION anclax.reject_cleanup_test() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.spec->>'type' = 'cleanupWorkerCommandTasks' THEN
        RAISE EXCEPTION 'cleanup enqueue rejected';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER reject_cleanup_test BEFORE INSERT ON anclax.tasks
FOR EACH ROW EXECUTE FUNCTION anclax.reject_cleanup_test();`)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := conn.Exec(ctx, "DROP TRIGGER IF EXISTS reject_cleanup_test ON anclax.tasks; DROP FUNCTION IF EXISTS anclax.reject_cleanup_test()")
				require.NoError(t, err)
			})
			require.ErrorContains(t, port.FinalizeTask(ctx, *parent, nil), "cleanup enqueue rejected")
			row, err := m.GetTaskByID(ctx, parentID)
			require.NoError(t, err)
			require.Equal(t, "running", row.Status, "parent terminal state must roll back if cleanup cannot be enqueued")
			cleanupTag := fmt.Sprintf("broadcast:cleanup_worker_commands:%d", parentID)
			_, err = m.GetTaskByUniqueTag(ctx, &cleanupTag)
			require.ErrorIs(t, err, pgx.ErrNoRows)
			_, err = conn.Exec(ctx, "DROP TRIGGER reject_cleanup_test ON anclax.tasks")
			require.NoError(t, err)
			require.NoError(t, port.FinalizeTask(ctx, *parent, nil))
			row, err = m.GetTaskByID(ctx, parentID)
			require.NoError(t, err)
			require.Equal(t, "completed", row.Status)
			_, err = m.GetTaskByUniqueTag(ctx, &cleanupTag)
			require.NoError(t, err)
		})
	})
}

func TestWorkerCommandCleanupMigrationSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		source, err := iofs.New(anclax.Migrations, "sql/migrations")
		require.NoError(t, err)
		dsn := strings.Replace(smokePostgresDSN(), "postgres://", "pgx5://", 1) + "&x-migrations-table=anchor_migrations"
		migration, err := migrate.NewWithSourceInstance("iofs", source, dsn)
		require.NoError(t, err)
		defer migration.Close()
		runner := taskgen.NewTaskRunner(taskstore.NewTaskStore(m))
		cleanupID, err := runner.RunCleanupWorkerCommandTasks(ctx, &taskgen.CleanupWorkerCommandTasksParameters{ParentTaskID: 42})
		require.NoError(t, err)
		require.NoError(t, migration.Migrate(16))
		row, err := m.GetTaskByID(ctx, cleanupID)
		require.NoError(t, err)
		require.Equal(t, "cancelled", row.Status, "rollback retains history and prevents old workers claiming unsupported tasks")
		require.NoError(t, migration.Up())
		newID, err := runner.RunCleanupWorkerCommandTasks(ctx, &taskgen.CleanupWorkerCommandTasksParameters{ParentTaskID: 42})
		require.NoError(t, err)
		port, err := worker.NewModelPort(m, uuid.New(), nil, nil, time.Minute, 0)
		require.NoError(t, err)
		claimed, err := port.ClaimControl(ctx, worker.ClaimRequest{})
		require.NoError(t, err)
		require.Equal(t, newID, claimed.ID, "cleanup is claimable through the system lane after reapplying the migration")
	})
}
