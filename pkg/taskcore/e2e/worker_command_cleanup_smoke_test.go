//go:build smoke

package taskcoree2e_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudcarver/anclax"
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
					port, err := worker.NewModelPort(m, uuid.New(), nil, handler, time.Minute, 0)
					require.NoError(t, err)
					parent, err := port.ClaimControl(ctx, worker.ClaimRequest{})
					require.NoError(t, err)
					require.Equal(t, parentID, parent.ID)
					execErr := port.ExecuteTask(ctx, *parent)
					var deferred *taskstore.TaskDeferred
					require.ErrorAs(t, execErr, &deferred)
					require.NoError(t, port.FinalizeTask(ctx, *parent, execErr))
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
					// Repeating delivery after enqueue, before committing the parent
					// result, must reuse the existing cleanup task.
					require.NoError(t, handler.HandleTask(ctx, *parent))
					cleanupTag := fmt.Sprintf("broadcast:cleanup_worker_commands:%d", parentID)
					var cleanupCount int
					require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE unique_tag=$1", cleanupTag).Scan(&cleanupCount))
					require.Equal(t, 1, cleanupCount)
					cleanupPort, err := worker.NewModelPort(m, uuid.New(), nil, handler, time.Minute, 0)
					require.NoError(t, err)
					cleanup, err := cleanupPort.ClaimControl(ctx, worker.ClaimRequest{})
					require.NoError(t, err)
					require.Equal(t, taskgen.CleanupWorkerCommandTasks, cleanup.GetType())
					require.True(t, worker.IsControlTask(cleanup.GetType()))
					require.Nil(t, cleanup.Attributes.Labels)
					execErr = cleanupPort.ExecuteTask(ctx, *cleanup)
					require.ErrorAs(t, execErr, &deferred, "cleanup must wait for parent finalization")
					require.NoError(t, cleanupPort.FinalizeTask(ctx, *cleanup, execErr))
					unchanged, err := m.GetTaskByID(ctx, child.ID)
					require.NoError(t, err)
					require.Equal(t, childStatus, unchanged.Status)
					require.NoError(t, port.FinalizeTask(ctx, *parent, nil))
					require.NoError(t, m.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{ID: cleanup.ID}))
					cleanup, err = cleanupPort.ClaimControl(ctx, worker.ClaimRequest{})
					require.NoError(t, err)
					require.Equal(t, taskgen.CleanupWorkerCommandTasks, cleanup.GetType())

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
