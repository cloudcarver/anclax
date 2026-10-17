//go:build smoke

package taskcoree2e_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/asynctask"
	"github.com/cloudcarver/anclax/pkg/config"
	taskstore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestWorkerCommandCancellationSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		store := taskstore.NewTaskStore(m)
		runner := taskgen.NewTaskRunner(store)
		handler := taskgen.NewTaskHandler(asynctask.NewExecutor(&config.Config{}, m, runner))
		for _, command := range []string{"cancel", "pause"} {
			for _, childStatus := range []string{"pending", "running"} {
				t.Run(command+"/"+childStatus, func(t *testing.T) {
					require.NoError(t, resetDSTState(ctx, m))
					targetID := uuid.New()
					_, err := m.UpsertWorker(ctx, querier.UpsertWorkerParams{ID: targetID, Labels: []byte(`[]`)})
					require.NoError(t, err)
					var parentID int32
					if command == "cancel" {
						parentID, err = runner.RunBroadcastCancelTask(ctx, &taskgen.BroadcastCancelTaskParameters{TaskIDs: []int32{7}, WorkerIDs: []uuid.UUID{targetID}}, taskstore.WithPriority(0))
					} else {
						parentID, err = runner.RunBroadcastPauseTask(ctx, &taskgen.BroadcastPauseTaskParameters{TaskIDs: []int32{7}, WorkerIDs: []uuid.UUID{targetID}}, taskstore.WithPriority(0))
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
						targetPort, err := worker.NewModelPort(m, targetID, []string{"worker:" + targetID.String()}, nil, time.Minute, 0)
						require.NoError(t, err)
						claimed, err := targetPort.ClaimControl(ctx, worker.ClaimRequest{})
						require.NoError(t, err)
						require.Equal(t, child.ID, claimed.ID)
					}
					// The target disappears before the next attempt filters its snapshot.
					_, err = conn.Exec(ctx, "UPDATE anclax.workers SET last_heartbeat=statement_timestamp()-interval '1 minute' WHERE id=$1", targetID)
					require.NoError(t, err)
					require.NoError(t, m.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{ID: parentID}))
					parent, err = port.ClaimControl(ctx, worker.ClaimRequest{})
					require.NoError(t, err)
					require.Equal(t, parentID, parent.ID)
					require.NoError(t, port.ExecuteTask(ctx, *parent))
					child, err = m.GetTaskByID(ctx, child.ID)
					require.NoError(t, err, "the command record is retained")
					require.Equal(t, "cancelled", child.Status, "cancellation happens directly before parent finalization")
					require.NoError(t, handler.HandleTask(ctx, *parent), "repeated delivery is idempotent")
					require.NoError(t, port.FinalizeTask(ctx, *parent, nil))
					finished, err := m.GetTaskByID(ctx, parentID)
					require.NoError(t, err)
					require.Equal(t, "completed", finished.Status)
					var taskCount int
					require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks").Scan(&taskCount))
					require.Equal(t, 2, taskCount, "only the broadcast and its command are created")
				})
			}
		}
		t.Run("conditional cancellation preserves terminal and unrelated records", func(t *testing.T) {
			require.NoError(t, resetDSTState(ctx, m))
			for _, taskType := range []string{taskgen.CancelTaskOnWorker, taskgen.PauseTaskOnWorker, taskgen.ApplyWorkerRuntimeConfigToWorker, "business-task"} {
				for _, status := range []apigen.TaskStatus{apigen.Pending, apigen.TaskStatusReady, apigen.TaskStatusRunning, apigen.Paused, apigen.Completed, apigen.Failed, apigen.Cancelled} {
					tag := taskType + "/" + string(status)
					initialStatus := status
					if status == apigen.TaskStatusReady {
						initialStatus = apigen.Pending
					}
					id, err := store.PushTask(ctx, &apigen.Task{UniqueTag: &tag, Status: initialStatus, Spec: apigen.TaskSpec{Type: taskType, Payload: []byte(`{}`)}})
					require.NoError(t, err)
					if status == apigen.TaskStatusReady {
						// ready is scheduler-owned and cannot be supplied at enqueue.
						_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET status='ready' WHERE id=$1", id)
						require.NoError(t, err)
					}
					require.NoError(t, m.CancelWorkerCommandTaskByUniqueTag(ctx, &tag))
					require.NoError(t, m.CancelWorkerCommandTaskByUniqueTag(ctx, &tag))
					row, err := m.GetTaskByID(ctx, id)
					require.NoError(t, err)
					want := status
					if taskType != "business-task" && status != apigen.Completed && status != apigen.Failed && status != apigen.Cancelled {
						want = apigen.Cancelled
					}
					require.Equal(t, string(want), row.Status, "%s", tag)
				}
			}
			missing := "missing-command"
			require.NoError(t, m.CancelWorkerCommandTaskByUniqueTag(ctx, &missing))
		})
		t.Run("concurrent completion is preserved", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			tag := "concurrent-command"
			id, err := store.PushTask(ctx, &apigen.Task{UniqueTag: &tag, Status: apigen.TaskStatusRunning, Spec: apigen.TaskSpec{Type: taskgen.CancelTaskOnWorker, Payload: []byte(`{}`)}})
			require.NoError(t, err)
			canceller, err := pgx.Connect(ctx, smokePostgresDSN())
			require.NoError(t, err)
			defer canceller.Close(ctx)
			tx, err := conn.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx)
			_, err = tx.Exec(ctx, "UPDATE anclax.tasks SET status='completed' WHERE id=$1", id)
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() { done <- querier.New(canceller).CancelWorkerCommandTaskByUniqueTag(ctx, &tag) }()
			require.Eventually(t, func() bool {
				_, err := conn.Exec(ctx, "SELECT pg_stat_clear_snapshot()")
				require.NoError(t, err)
				var waiting bool
				err = conn.QueryRow(ctx, "SELECT COALESCE(wait_event_type = 'Lock', false) FROM pg_stat_activity WHERE pid=$1", canceller.PgConn().PID()).Scan(&waiting)
				require.NoError(t, err)
				return waiting
			}, 3*time.Second, 10*time.Millisecond, "cancellation must overlap the unfinished completion transaction")
			require.NoError(t, tx.Commit(ctx))
			require.NoError(t, <-done)
			row, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "completed", row.Status)
		})
	})
}
