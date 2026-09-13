//go:build smoke

package taskcoree2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/taskcore/ctrl"
	"github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type tagIgnoringCancelHandler struct {
	sqlErrorHook
	started, cancelled, release chan struct{}
}

func (h *tagIgnoringCancelHandler) HandleTask(ctx context.Context, _ worker.Task) error {
	close(h.started)
	<-ctx.Done()
	close(h.cancelled)
	<-h.release
	return nil
}

func TestTaskTagConcurrencySmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		s := store.NewTaskStore(m)
		control := ctrl.NewWorkerControlPlane(m, nil, s, nil)
		reset := func(t *testing.T) { t.Helper(); require.NoError(t, resetDSTState(ctx, m)) }
		enqueue := func(t *testing.T, tags ...string) int32 {
			t.Helper()
			id, err := s.PushTask(ctx, &apigen.Task{Status: apigen.Pending, Spec: apigen.TaskSpec{Type: "tag-probe", Payload: []byte(`{}`)}, Attributes: apigen.TaskAttributes{Tags: &tags}})
			require.NoError(t, err)
			return id
		}
		port := func(t *testing.T, ttl time.Duration) *worker.ModelPort {
			t.Helper()
			p, err := worker.NewModelPort(m, uuid.New(), nil, nil, ttl, 0)
			require.NoError(t, err)
			return p
		}
		claim := func(t *testing.T, p *worker.ModelPort, id int32) *worker.Task {
			t.Helper()
			task, err := p.ClaimByID(ctx, id, worker.ClaimRequest{AllowStrict: true})
			require.NoError(t, err)
			return task
		}
		blocked := func(t *testing.T, p *worker.ModelPort, id int32) {
			t.Helper()
			_, err := p.ClaimByID(ctx, id, worker.ClaimRequest{AllowStrict: true})
			require.ErrorIs(t, err, worker.ErrNoTask)
		}
		usage := func(t *testing.T, tag string, want int32) {
			t.Helper()
			state, err := control.GetTagConcurrency(ctx, tag)
			require.NoError(t, err)
			require.Equal(t, want, state.InUse, "tag %s", tag)
			var permits int32
			require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.task_tag_permits WHERE tag = $1", tag).Scan(&permits))
			require.Equal(t, want, permits)
		}
		membership := func(t *testing.T, id int32, tags ...string) {
			t.Helper()
			var got []string
			require.NoError(t, conn.QueryRow(ctx, "SELECT COALESCE(array_agg(tag ORDER BY tag), ARRAY[]::text[]) FROM anclax.task_tags WHERE task_id = $1", id).Scan(&got))
			require.ElementsMatch(t, tags, got)
		}

		t.Run("terminal_finalization_removes_membership_and_preserves_history_and_rules", func(t *testing.T) {
			for _, status := range []string{"completed", "failed", "cancelled"} {
				t.Run(status, func(t *testing.T) {
					reset(t)
					require.NoError(t, control.SetTagConcurrencyLimit(ctx, "a", 1))
					p := port(t, 9*time.Second)
					id := enqueue(t, "a", "b")
					task := claim(t, p, id)
					var result error
					switch status {
					case "failed":
						result = errors.New("executor failed")
					case "cancelled":
						require.NoError(t, s.CancelTask(ctx, id))
						result = store.ErrTaskCancelled
					}
					membership(t, id, "a", "b")
					usage(t, "a", 1)
					require.NoError(t, p.FinalizeTask(ctx, *task, result))
					membership(t, id)
					usage(t, "a", 0)
					usage(t, "b", 0)
					row, err := m.GetTaskByID(ctx, id)
					require.NoError(t, err)
					require.Equal(t, status, row.Status)
					require.Equal(t, []string{"a", "b"}, *row.Attributes.Tags)
					ids, err := m.ListTaskIDsByTags(ctx, querier.ListTaskIDsByTagsParams{Tags: []string{"a", "b"}, ExceptTagSets: []byte("[]")})
					require.NoError(t, err)
					require.Equal(t, []int32{id}, ids)
					state, err := control.GetTagConcurrency(ctx, "a")
					require.NoError(t, err)
					require.NotNil(t, state.MaxConcurrency)
					require.Equal(t, int32(1), *state.MaxConcurrency)
				})
			}
		})

		t.Run("historical_insert_and_edit_skip_membership_and_restore_rebuilds_it", func(t *testing.T) {
			reset(t)
			tags := []string{"history"}
			id, err := s.PushTask(ctx, &apigen.Task{Status: apigen.Completed, Spec: apigen.TaskSpec{Type: "tag-probe", Payload: []byte(`{}`)}, Attributes: apigen.TaskAttributes{Tags: &tags}})
			require.NoError(t, err)
			membership(t, id)
			_, err = m.GetTaskTagConcurrency(ctx, "history")
			require.ErrorIs(t, err, pgx.ErrNoRows)
			_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET attributes = jsonb_set(attributes, '{tags}', '["restored"]') WHERE id = $1`, id)
			require.NoError(t, err)
			membership(t, id)
			_, err = m.GetTaskTagConcurrency(ctx, "restored")
			require.ErrorIs(t, err, pgx.ErrNoRows)
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "restored", 0))
			// The public resume API intentionally rejects terminal tasks, but
			// low-level restoration must still rebuild admission membership.
			_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET status = 'pending' WHERE id = $1", id)
			require.NoError(t, err)
			membership(t, id, "restored")
			p := port(t, 9*time.Second)
			blocked(t, p, id)
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "restored", 1))
			task := claim(t, p, id)
			usage(t, "restored", 1)
			require.NoError(t, p.FinalizeTask(ctx, *task, nil))
			membership(t, id)
		})

		t.Run("terminal_lease_recovery_removes_membership", func(t *testing.T) {
			for _, status := range []string{"completed", "failed", "cancelled"} {
				t.Run(status, func(t *testing.T) {
					reset(t)
					p := port(t, 9*time.Second)
					id := enqueue(t, "a", "b")
					claim(t, p, id)
					require.NoError(t, m.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{ID: id, Status: status}))
					membership(t, id, "a", "b")
					usage(t, "a", 1)
					_, err := conn.Exec(ctx, "UPDATE anclax.tasks SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1", id)
					require.NoError(t, err)
					require.NoError(t, m.MaintainTaskConcurrency(ctx, 9000))
					membership(t, id)
					usage(t, "a", 0)
					usage(t, "b", 0)
				})
			}
		})

		t.Run("pending_running_paused_retry_and_cron_keep_membership", func(t *testing.T) {
			reset(t)
			p := port(t, 9*time.Second)
			for _, status := range []apigen.TaskStatus{apigen.Pending, apigen.TaskStatus("running"), apigen.Paused} {
				tags := []string{"a", "b"}
				id, err := s.PushTask(ctx, &apigen.Task{Status: status, Spec: apigen.TaskSpec{Type: "tag-probe", Payload: []byte(`{}`)}, Attributes: apigen.TaskAttributes{Tags: &tags}})
				require.NoError(t, err)
				membership(t, id, "a", "b")
			}
			id := enqueue(t, "a", "b")
			require.NoError(t, s.PauseTask(ctx, id))
			membership(t, id, "a", "b")
			require.NoError(t, s.ResumeTask(ctx, id))
			membership(t, id, "a", "b")
			_, err := conn.Exec(ctx, `UPDATE anclax.tasks SET attributes = attributes || '{"retryPolicy":{"interval":"1h","maxAttempts":2}}'::jsonb WHERE id = $1`, id)
			require.NoError(t, err)
			task := claim(t, p, id)
			require.NoError(t, p.FinalizeTask(ctx, *task, errors.New("retry")))
			membership(t, id, "a", "b")
			usage(t, "a", 0)
			id = enqueue(t, "a", "b")
			_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET attributes = attributes || '{"cronjob":{"cronExpression":"0 0 * * * *"}}'::jsonb WHERE id = $1`, id)
			require.NoError(t, err)
			task = claim(t, p, id)
			require.NoError(t, p.FinalizeTask(ctx, *task, nil))
			membership(t, id, "a", "b")
			usage(t, "a", 0)
		})

		t.Run("all_tags_or_none_and_no_retry_consumption", func(t *testing.T) {
			reset(t)
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "a", 1))
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "b", 1))
			p := port(t, 9*time.Second)
			a := claim(t, p, enqueue(t, "a"))
			id := enqueue(t, "b", "a", "a")
			blocked(t, p, id)
			usage(t, "a", 1)
			usage(t, "b", 0)
			row, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Zero(t, row.Attempts)
			require.Zero(t, row.LeaseVersion)
			require.Nil(t, row.LockedAt)
			require.Equal(t, []string{"b", "a", "a"}, *row.Attributes.Tags)
			require.NoError(t, p.FinalizeTask(ctx, *a, nil))
			row, err = m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Nil(t, row.ConcurrencyWaitTag, "release should wake the waiter immediately")
			both := claim(t, p, id)
			usage(t, "a", 1)
			usage(t, "b", 1)
			require.NoError(t, p.FinalizeTask(ctx, *both, nil))
			usage(t, "a", 0)
			usage(t, "b", 0)
		})

		t.Run("enable_lower_remove_and_reenable_include_existing_attempts", func(t *testing.T) {
			reset(t)
			p := port(t, 9*time.Second)
			first := claim(t, p, enqueue(t, "tenant:1"))
			second := claim(t, p, enqueue(t, "tenant:1"))
			usage(t, "tenant:1", 2)
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "tenant:1", 1))
			id := enqueue(t, "tenant:1")
			blocked(t, p, id)
			require.NoError(t, p.FinalizeTask(ctx, *first, nil))
			blocked(t, p, id)
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "tenant:1", 0))
			require.NoError(t, p.FinalizeTask(ctx, *second, nil))
			blocked(t, p, id)
			require.NoError(t, control.RemoveTagConcurrencyLimit(ctx, "tenant:1"))
			row, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Nil(t, row.ConcurrencyWaitTag)
			third := claim(t, p, id)
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "tenant:1", 1))
			blocked(t, p, enqueue(t, "tenant:1"))
			usage(t, "tenant:1", 1)
			require.NoError(t, p.FinalizeTask(ctx, *third, nil))
		})

		t.Run("tag_changes_keep_attempt_snapshot_and_stale_finalize_is_fenced", func(t *testing.T) {
			reset(t)
			p := port(t, 9*time.Second)
			id := enqueue(t, "old")
			old := claim(t, p, id)
			_, err := conn.Exec(ctx, `UPDATE anclax.tasks SET attributes = jsonb_set(attributes, '{tags}', '["new"]') WHERE id = $1`, id)
			require.NoError(t, err)
			usage(t, "old", 1)
			usage(t, "new", 0)
			require.NoError(t, p.FinalizeTask(ctx, *old, store.ErrTaskInterrupted))
			usage(t, "old", 0)
			require.NoError(t, m.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{ID: id}))
			current := claim(t, p, id)
			require.Greater(t, current.LeaseVersion, old.LeaseVersion)
			require.NoError(t, p.FinalizeTask(ctx, *old, nil))
			usage(t, "new", 1)
			require.NoError(t, p.FinalizeTask(ctx, *current, nil))
			usage(t, "new", 0)
		})

		t.Run("expiry_uses_owner_ttl_and_expired_renewal_cannot_revive_permits", func(t *testing.T) {
			reset(t)
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "a", 1))
			p := port(t, 9*time.Second)
			id := enqueue(t, "a")
			old := claim(t, p, id)
			short := port(t, time.Millisecond)
			time.Sleep(3 * time.Millisecond)
			blocked(t, short, id)
			usage(t, "a", 1)
			row, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1", id)
			require.NoError(t, err)
			_, err = m.RefreshTaskLock(ctx, querier.RefreshTaskLockParams{ID: id, WorkerID: row.WorkerID, LeaseVersion: old.LeaseVersion})
			require.ErrorIs(t, err, pgx.ErrNoRows)
			require.NoError(t, m.MaintainTaskConcurrency(ctx, 9000))
			usage(t, "a", 0)
			current := claim(t, p, id)
			require.NoError(t, p.FinalizeTask(ctx, *old, nil))
			usage(t, "a", 1)
			require.NoError(t, p.FinalizeTask(ctx, *current, nil))
		})

		t.Run("resume_retains_invalidated_permits_until_expiry", func(t *testing.T) {
			reset(t)
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "a", 1))
			p := port(t, 9*time.Second)
			id := enqueue(t, "a")
			old := claim(t, p, id)
			require.NoError(t, s.PauseTask(ctx, id))
			require.NoError(t, s.ResumeTask(ctx, id))
			require.NoError(t, p.FinalizeTask(ctx, *old, store.ErrTaskPaused))
			usage(t, "a", 1)
			blocked(t, p, id)
			other := enqueue(t, "a")
			blocked(t, p, other)
			_, err := conn.Exec(ctx, "UPDATE anclax.tasks SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1", id)
			require.NoError(t, err)
			require.NoError(t, m.MaintainTaskConcurrency(ctx, 9000))
			usage(t, "a", 0)
			next := claim(t, p, other)
			require.NoError(t, p.FinalizeTask(ctx, *old, nil))
			usage(t, "a", 1)
			require.NoError(t, p.FinalizeTask(ctx, *next, nil))
		})

		t.Run("renewal_and_reaping_share_task_lock", func(t *testing.T) {
			reset(t)
			p := port(t, 9*time.Second)
			id := enqueue(t, "a", "b")
			task := claim(t, p, id)
			row, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET lease_expires_at = statement_timestamp() + interval '100 milliseconds' WHERE id = $1", id)
			require.NoError(t, err)
			tx, err := conn.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx)
			_, err = querier.New(tx).RefreshTaskLock(ctx, querier.RefreshTaskLockParams{ID: id, WorkerID: row.WorkerID, LeaseVersion: task.LeaseVersion})
			require.NoError(t, err)
			time.Sleep(120 * time.Millisecond)
			require.NoError(t, m.MaintainTaskConcurrency(ctx, 9000))
			require.NoError(t, tx.Commit(ctx))
			usage(t, "a", 1)
			usage(t, "b", 1)
			require.NoError(t, p.FinalizeTask(ctx, *task, nil))
		})

		t.Run("cancel_holds_capacity_until_handler_exits", func(t *testing.T) {
			reset(t)
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "a", 1))
			h := &tagIgnoringCancelHandler{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
			p, err := worker.NewModelPort(m, uuid.New(), nil, h, 9*time.Second, 10*time.Millisecond)
			require.NoError(t, err)
			first := claim(t, p, enqueue(t, "a"))
			exited := make(chan error, 1)
			go func() { exited <- p.ExecuteTask(ctx, *first) }()
			select {
			case <-h.started:
			case <-time.After(time.Second):
				t.Fatal("handler did not start")
			}
			require.NoError(t, s.CancelTask(ctx, first.ID))
			select {
			case <-h.cancelled:
			case <-time.After(time.Second):
				t.Fatal("handler did not receive cancellation")
			}
			usage(t, "a", 1)
			id := enqueue(t, "a")
			blocked(t, p, id)
			close(h.release)
			require.NoError(t, p.FinalizeTask(ctx, *first, <-exited))
			usage(t, "a", 0)
			next := claim(t, p, id)
			require.NoError(t, p.FinalizeTask(ctx, *next, nil))
		})

		t.Run("rollback_retry_cron_delete_and_control_bypass", func(t *testing.T) {
			reset(t)
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "a", 0))
			p := port(t, 9*time.Second)
			for _, taskType := range []string{"broadcastUpdateWorkerRuntimeConfig", "applyWorkerRuntimeConfigToWorker", "broadcastCancelTask", "cancelTaskOnWorker", "broadcastPauseTask", "pauseTaskOnWorker"} {
				id := enqueue(t, "a")
				_, err := conn.Exec(ctx, "UPDATE anclax.tasks SET spec = jsonb_set(spec, '{type}', to_jsonb($2::text)), concurrency_wait_tag = NULL WHERE id = $1", id, taskType)
				require.NoError(t, err)
				task, err := p.ClaimControl(ctx, worker.ClaimRequest{})
				require.NoError(t, err)
				require.Equal(t, id, task.ID)
				usage(t, "a", 0)
				require.NoError(t, p.FinalizeTask(ctx, *task, nil))
			}
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "a", 1))
			id := enqueue(t, "a")
			rollback := errors.New("rollback")
			err := m.RunTransactionWithTx(ctx, func(_ core.Tx, txm model.ModelInterface) error {
				_, err := txm.ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{ID: id, WorkerID: uuid.NullUUID{UUID: uuid.New(), Valid: true}, LockTtlMs: 9000})
				if err != nil {
					return err
				}
				return rollback
			})
			require.ErrorIs(t, err, rollback)
			usage(t, "a", 0)
			_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET attributes = attributes || '{"cronjob":{"cronExpression":"0 0 * * * *"}}'::jsonb WHERE id = $1`, id)
			require.NoError(t, err)
			task := claim(t, p, id)
			require.NoError(t, p.FinalizeTask(ctx, *task, nil))
			usage(t, "a", 0)
			row, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "pending", row.Status)
			require.NotNil(t, row.StartedAt)
			next := claim(t, p, enqueue(t, "a"))
			_, err = conn.Exec(ctx, "DELETE FROM anclax.tasks WHERE id = $1", next.ID)
			require.NoError(t, err)
			usage(t, "a", 0)
		})

		t.Run("multi_worker_two_tag_contention", func(t *testing.T) {
			reset(t)
			for tag, cap := range map[string]int32{"tenant": 3, "type:a": 2, "type:b": 2} {
				require.NoError(t, control.SetTagConcurrencyLimit(ctx, tag, cap))
			}
			// Inspect every counter increment, rather than sampling and missing
			// short-lived oversubscription. This assertion exists only in tests.
			_, err := conn.Exec(ctx, `CREATE FUNCTION anclax.assert_tag_capacity() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
                IF NEW.in_use > OLD.in_use AND NEW.in_use > NEW.max_concurrency THEN RAISE EXCEPTION 'tag capacity exceeded: %', NEW.tag; END IF;
                IF NEW.in_use <> (SELECT count(*) FROM anclax.task_tag_permits WHERE tag = NEW.tag) THEN RAISE EXCEPTION 'tag counter drift: %', NEW.tag; END IF;
                RETURN NEW; END $$;
                CREATE TRIGGER assert_tag_capacity AFTER UPDATE OF in_use ON anclax.task_tag_concurrency FOR EACH ROW EXECUTE FUNCTION anclax.assert_tag_capacity();`)
			require.NoError(t, err)
			defer func() {
				_, err := conn.Exec(ctx, "DROP TRIGGER assert_tag_capacity ON anclax.task_tag_concurrency; DROP FUNCTION anclax.assert_tag_capacity()")
				require.NoError(t, err)
			}()
			const count = 96
			for i := 0; i < count; i++ {
				if i%2 == 0 {
					enqueue(t, "tenant", "type:a")
				} else {
					enqueue(t, "type:b", "tenant")
				}
			}
			runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			var completed, active, peak atomic.Int32
			var wg sync.WaitGroup
			errs := make(chan error, 17)
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 40; i++ {
					if err := control.SetTagConcurrencyLimit(runCtx, "tenant", int32(i%4)); err != nil {
						errs <- err
						return
					}
					time.Sleep(time.Millisecond)
				}
				if err := control.SetTagConcurrencyLimit(runCtx, "tenant", 3); err != nil {
					errs <- err
				}
			}()
			for i := 0; i < 16; i++ {
				p := port(t, 9*time.Second)
				wg.Add(1)
				go func() {
					defer wg.Done()
					for completed.Load() < count && runCtx.Err() == nil {
						task, err := p.ClaimNormalByGroup(runCtx, worker.ClaimNormalRequest{Group: worker.DefaultWeightGroup})
						if errors.Is(err, worker.ErrNoTask) {
							time.Sleep(2 * time.Millisecond)
							continue
						}
						if err != nil {
							errs <- err
							return
						}
						n := active.Add(1)
						for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
						}
						time.Sleep(2 * time.Millisecond)
						active.Add(-1)
						if err := p.FinalizeTask(runCtx, *task, nil); err != nil {
							errs <- err
							return
						}
						completed.Add(1)
					}
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			require.Equal(t, int32(count), completed.Load())
			require.LessOrEqual(t, peak.Load(), int32(3))
			t.Logf("16 workers, %d tasks, 2 tags/task: peak running=%d", count, peak.Load())
			for _, tag := range []string{"tenant", "type:a", "type:b"} {
				usage(t, tag, 0)
			}
		})

		t.Run("bounded_candidates_indexed_waiters_and_fallback", func(t *testing.T) {
			reset(t)
			// Existing backlog is visited in bounded batches when a new rule
			// blocks it. New arrivals are parked directly at enqueue time.
			for i := 0; i < 70; i++ {
				enqueue(t, "hot")
			}
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "hot", 0))
			cold := enqueue(t, "cold")
			p := port(t, 9*time.Second)
			for _, want := range []int{32, 64} {
				_, err := p.ClaimNormalByGroup(ctx, worker.ClaimNormalRequest{Group: worker.DefaultWeightGroup})
				require.ErrorIs(t, err, worker.ErrNoTask)
				var waiting int
				require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE concurrency_wait_tag = 'hot'").Scan(&waiting))
				require.Equal(t, want, waiting)
			}
			task, err := p.ClaimNormalByGroup(ctx, worker.ClaimNormalRequest{Group: worker.DefaultWeightGroup})
			require.NoError(t, err)
			require.Equal(t, cold, task.ID)
			require.NoError(t, p.FinalizeTask(ctx, *task, nil))
			_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
                SELECT '{"tags":["hot","kind:a"]}', '{"type":"tag-probe","payload":{}}', 'pending' FROM generate_series(1,20000)`)
			require.NoError(t, err)
			cold = enqueue(t, "cold")
			_, err = conn.Exec(ctx, "ANALYZE anclax.tasks")
			require.NoError(t, err)
			var plan json.RawMessage
			require.NoError(t, conn.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
                SELECT id FROM anclax.tasks WHERE status='pending' AND concurrency_wait_tag IS NULL
                ORDER BY priority DESC, created_at, id LIMIT 32 FOR UPDATE SKIP LOCKED`).Scan(&plan))
			require.True(t, strings.Contains(string(plan), "idx_tasks_pending_priority_created"), string(plan))
			start := time.Now()
			task, err = p.ClaimNormalByGroup(ctx, worker.ClaimNormalRequest{Group: worker.DefaultWeightGroup})
			require.NoError(t, err)
			require.Equal(t, cold, task.ID)
			t.Logf("20,070 parked tasks, 1 ready task: claim=%s; ready-index plan=%s", time.Since(start), plan)
			require.NoError(t, p.FinalizeTask(ctx, *task, nil))
			// A wakeup missed because the task was locked must eventually
			// recover via the retry index, without changing business started_at.
			_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET concurrency_retry_at = statement_timestamp() - interval '1 second' WHERE concurrency_wait_tag IS NOT NULL")
			require.NoError(t, err)
			require.NoError(t, m.MaintainTaskConcurrency(ctx, 9000))
			var ready int
			require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE status='pending' AND concurrency_wait_tag IS NULL").Scan(&ready))
			require.Zero(t, ready, "full tags must stay out of the ready index during fallback")
			require.NoError(t, control.SetTagConcurrencyLimit(ctx, "hot", 64))
			// Model missed notifications by re-parking the awakened rows.
			_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET concurrency_wait_tag = 'hot', concurrency_retry_at = statement_timestamp() - interval '1 second' WHERE status='pending'")
			require.NoError(t, err)
			require.NoError(t, m.MaintainTaskConcurrency(ctx, 9000))
			require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE status='pending' AND concurrency_wait_tag IS NULL").Scan(&ready))
			require.Equal(t, 64, ready)
		})
	})
}
