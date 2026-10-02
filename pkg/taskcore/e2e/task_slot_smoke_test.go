//go:build smoke

package taskcoree2e_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestTaskSlotAdmissionSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		limit := func(t *testing.T, tag string, capacity int32) {
			require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: tag, MaxConcurrency: capacity}))
		}
		enqueue := func(t *testing.T, tags string) int32 {
			var id int32
			require.NoError(t, conn.QueryRow(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
				VALUES (jsonb_build_object('tags',$1::jsonb),'{"type":"slot-probe"}','pending') RETURNING id`, tags).Scan(&id))
			return id
		}
		port := func(t *testing.T) *worker.ModelPort {
			p, err := worker.NewModelPort(m, uuid.New(), nil, nil, 9*time.Second, 0)
			require.NoError(t, err)
			return p
		}
		claim := func(t *testing.T, p *worker.ModelPort, id int32) *worker.Task {
			task, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			require.NoError(t, err)
			return task
		}
		usage := func(t *testing.T, tag string, want int32) {
			state, err := m.GetTaskTagConcurrency(ctx, tag)
			require.NoError(t, err)
			require.Equal(t, want, state.InUse)
		}

		t.Run("generic_control_claim_bypasses_resource_prefilter", func(t *testing.T) {
			require.NoError(t, resetDSTState(ctx, m))
			limit(t, "full", 0)
			id := enqueue(t, `["full"]`)
			_, err := conn.Exec(ctx, `UPDATE anclax.tasks SET spec='{"type":"broadcastCancelTask"}' WHERE id=$1`, id)
			require.NoError(t, err)
			tx, err := conn.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx)
			task, err := querier.New(tx).ClaimTask(ctx, querier.ClaimTaskParams{WorkerID: uuid.NullUUID{UUID: uuid.New(), Valid: true}, LockTtlMs: 9000})
			require.NoError(t, err)
			require.Equal(t, id, task.ID)
			require.NoError(t, tx.Commit(ctx))
			usage(t, "full", 0)
		})

		t.Run("finalizers_do_not_acquire_allocation_guards", func(t *testing.T) {
			require.NoError(t, resetDSTState(ctx, m))
			limit(t, "shared", 8)
			p := port(t)
			var tasks []*worker.Task
			for range 8 {
				tasks = append(tasks, claim(t, p, enqueue(t, `["shared"]`)))
			}
			waiting := enqueue(t, `["shared"]`)
			blocker, err := conn.Begin(ctx)
			require.NoError(t, err)
			defer blocker.Rollback(ctx)
			for slot := 1; slot <= 8; slot++ {
				_, err = blocker.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("anclax:task-slot:shared:%d", slot))
				require.NoError(t, err)
			}
			finalizeCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			errs := make(chan error, len(tasks))
			var wg sync.WaitGroup
			for _, task := range tasks {
				wg.Go(func() { errs <- p.FinalizeTask(finalizeCtx, *task, nil) })
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err, "release must not contend on allocation guards, including its own slot")
			}
			usage(t, "shared", 0)
			_, err = p.ClaimByID(ctx, waiting, worker.ClaimRequest{})
			require.ErrorIs(t, err, worker.ErrNoTask, "the same held guards must prevent allocation of the now-free slots")
			require.NoError(t, blocker.Commit(ctx))
			next := claim(t, p, waiting)
			require.NoError(t, p.FinalizeTask(ctx, *next, nil))
		})

		t.Run("uncommitted_allocator_does_not_block_another_slot_or_its_release", func(t *testing.T) {
			require.NoError(t, resetDSTState(ctx, m))
			limit(t, "shared", 2)
			first, second := enqueue(t, `["shared"]`), enqueue(t, `["shared"]`)
			tx, err := conn.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx)
			_, err = querier.New(tx).ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{ID: first, LockTtlMs: 9000, WorkerID: uuid.NullUUID{UUID: uuid.New(), Valid: true}})
			require.NoError(t, err)
			p := port(t)
			shortCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			task, err := p.ClaimByID(shortCtx, second, worker.ClaimRequest{})
			require.NoError(t, err)
			require.NoError(t, p.FinalizeTask(shortCtx, *task, nil))
			require.NoError(t, tx.Rollback(ctx))
			usage(t, "shared", 0)
		})

		t.Run("lowered_limit_allows_progress_as_soon_as_usage_falls_below_limit", func(t *testing.T) {
			require.NoError(t, resetDSTState(ctx, m))
			limit(t, "resize", 3)
			p := port(t)
			first := claim(t, p, enqueue(t, `["resize"]`))
			second := claim(t, p, enqueue(t, `["resize"]`))
			overflow := claim(t, p, enqueue(t, `["resize"]`))
			limit(t, "resize", 2)
			usage(t, "resize", 3)
			waiting := enqueue(t, `["resize"]`)
			require.NoError(t, p.FinalizeTask(ctx, *first, nil))
			_, err = p.ClaimByID(ctx, waiting, worker.ClaimRequest{})
			require.ErrorIs(t, err, worker.ErrNoTask)
			require.NoError(t, p.FinalizeTask(ctx, *second, nil))
			next := claim(t, p, waiting)
			usage(t, "resize", 2)
			waiting = enqueue(t, `["resize"]`)
			_, err = p.ClaimByID(ctx, waiting, worker.ClaimRequest{})
			require.ErrorIs(t, err, worker.ErrNoTask)
			require.NoError(t, p.FinalizeTask(ctx, *overflow, nil))
			last := claim(t, p, waiting)
			require.NoError(t, p.FinalizeTask(ctx, *next, nil))
			require.NoError(t, p.FinalizeTask(ctx, *last, nil))
			usage(t, "resize", 0)
		})

		t.Run("rejected_reclaim_preserves_the_previous_attempt_allocation", func(t *testing.T) {
			require.NoError(t, resetDSTState(ctx, m))
			limit(t, "old", 1)
			limit(t, "blocked", 0)
			p := port(t)
			task := claim(t, p, enqueue(t, `["old"]`))
			_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET attributes='{"tags":["blocked"]}',
				lease_expires_at=statement_timestamp()-interval '1 second' WHERE id=$1`, task.ID)
			require.NoError(t, err)
			var admitted bool
			require.NoError(t, conn.QueryRow(ctx, "SELECT anclax.try_admit_task_tags($1)", task.ID).Scan(&admitted))
			require.False(t, admitted)
			usage(t, "old", 1)
			usage(t, "blocked", 0)
			var version int64
			require.NoError(t, conn.QueryRow(ctx, "SELECT lease_version FROM anclax.task_tag_permits WHERE task_id=$1", task.ID).Scan(&version))
			require.Equal(t, task.LeaseVersion, version)
			require.NoError(t, m.MaintainTaskConcurrency(ctx, 9000))
			usage(t, "old", 0)
		})
	})
}
