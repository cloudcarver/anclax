//go:build smoke

package taskcoree2e_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestTaskAdmissionSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		reset := func(t *testing.T) { require.NoError(t, resetDSTState(ctx, m)) }
		enqueue := func(t *testing.T, tags string) int32 {
			var id int32
			require.NoError(t, conn.QueryRow(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
				VALUES (jsonb_build_object('tags',$1::jsonb),'{"type":"admission-probe"}','pending') RETURNING id`, tags).Scan(&id))
			return id
		}
		port := func(t *testing.T) *worker.ModelPort {
			p, err := worker.NewModelPort(m, uuid.New(), nil, nil, 9*time.Second, 0)
			require.NoError(t, err)
			return p
		}
		limit := func(t *testing.T, tag string, n int32) {
			require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: tag, MaxConcurrency: n}))
		}
		count := func(t *testing.T, query string, want int) {
			var n int
			require.NoError(t, conn.QueryRow(ctx, query).Scan(&n))
			require.Equal(t, want, n)
		}

		t.Run("admission_and_quota_changes_reject_stale_transaction_snapshots", func(t *testing.T) {
			reset(t)
			id := enqueue(t, `["isolation"]`)
			for _, isolation := range []pgx.TxIsoLevel{pgx.RepeatableRead, pgx.Serializable} {
				for _, query := range []string{
					"SELECT anclax.try_admit_task_tags($1)",
					"INSERT INTO anclax.task_tag_limits(tag,max_concurrency) VALUES ('isolation',$1)",
				} {
					tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
					require.NoError(t, err)
					_, err = tx.Exec(ctx, query, id)
					require.ErrorContains(t, err, "requires READ COMMITTED isolation")
					require.NoError(t, tx.Rollback(ctx))
				}
			}
			count(t, "SELECT count(*) FROM anclax.task_tag_concurrency", 0)
		})

		t.Run("serial_claim_plan_excludes_unleased_history", func(t *testing.T) {
			reset(t)
			_, err := conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,serial_key)
				SELECT '{}','{"type":"admission-probe"}','completed','history' FROM generate_series(1,100000);
				INSERT INTO anclax.tasks(attributes,spec,status,serial_key) VALUES ('{}','{"type":"admission-probe"}','pending','history')`)
			require.NoError(t, err)
			_, err = conn.Exec(ctx, "VACUUM ANALYZE anclax.tasks")
			require.NoError(t, err)
			tx, err := conn.Begin(ctx)
			require.NoError(t, err)
			capture := &captureClaimSQL{Tx: tx}
			_, err = querier.New(capture).ClaimNormalTaskByGroup(ctx, querier.ClaimNormalTaskByGroupParams{
				WorkerID: uuid.NullUUID{UUID: uuid.New(), Valid: true}, LockTtlMs: 9000, GroupName: worker.DefaultWeightGroup})
			require.NoError(t, err)
			require.NoError(t, tx.Rollback(ctx))
			tx, err = conn.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx)
			var plan string
			require.NoError(t, tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+capture.sql, capture.args...).Scan(&plan))
			require.Contains(t, plan, "idx_tasks_serial_leased", "real claim SQL must use the lease-only index")
			require.NotContains(t, plan, `"Rows Removed by Filter": 100000`)
			require.NoError(t, tx.Rollback(ctx))
			t.Logf("ClaimNormalTaskByGroup plan with 100000 historical serial tasks: %s", plan)
			tx, err = conn.Begin(ctx)
			require.NoError(t, err)
			capture = &captureClaimSQL{Tx: tx}
			_, err = querier.New(capture).ClaimTaskBatch(ctx, querier.ClaimTaskBatchParams{
				WorkerID: uuid.NullUUID{UUID: uuid.New(), Valid: true}, LockTtlMs: 9000, BatchSize: 32,
				GroupNames: []string{worker.DefaultWeightGroup}})
			require.NoError(t, err)
			require.NoError(t, tx.Rollback(ctx))
			tx, err = conn.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx)
			require.NoError(t, tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+capture.sql, capture.args...).Scan(&plan))
			require.Contains(t, plan, "idx_tasks_ready_", "ready claim no longer checks serial history")
			require.NotContains(t, plan, `"Rows Removed by Filter": 100000`)
			require.NoError(t, tx.Rollback(ctx))
			t.Logf("ClaimTaskBatch plan with 100000 historical serial tasks: %s", plan)
		})

		t.Run("unlimited_tags_have_no_shared_state_and_activation_uses_attempt_snapshot", func(t *testing.T) {
			reset(t)
			p := port(t)
			id := enqueue(t, `["ordinary","ordinary"]`)
			task, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			require.NoError(t, err)
			count(t, "SELECT count(*) FROM anclax.task_tag_permits", 0)
			count(t, "SELECT count(*) FROM anclax.task_tag_concurrency", 0)
			_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET attributes='{"tags":["edited"]}' WHERE id=$1`, id)
			require.NoError(t, err)
			limit(t, "ordinary", 1)
			limit(t, "edited", 1)
			count(t, "SELECT in_use FROM anclax.task_tag_concurrency WHERE tag='ordinary'", 1)
			count(t, "SELECT in_use FROM anclax.task_tag_concurrency WHERE tag='edited'", 0)
			require.NoError(t, m.RemoveTaskTagConcurrencyLimit(ctx, "ordinary"))
			count(t, "SELECT count(*) FROM anclax.task_tag_permits", 0)
			count(t, "SELECT in_use FROM anclax.task_tag_concurrency WHERE tag='ordinary'", 0)
			limit(t, "ordinary", 0)
			count(t, "SELECT in_use FROM anclax.task_tag_concurrency WHERE tag='ordinary'", 1)
			require.NoError(t, p.FinalizeTask(ctx, *task, nil))
			count(t, "SELECT count(*) FROM anclax.task_tag_permits", 0)
		})

		t.Run("quota_activation_waits_for_inflight_claim_before_backfill", func(t *testing.T) {
			reset(t)
			id := enqueue(t, `["live"]`)
			tx, err := conn.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx)
			_, err = querier.New(tx).ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{ID: id, LockTtlMs: 9000, WorkerID: uuid.NullUUID{UUID: uuid.New(), Valid: true}})
			require.NoError(t, err)
			setter, err := pgx.Connect(ctx, smokePostgresDSN())
			require.NoError(t, err)
			defer setter.Close(ctx)
			result := make(chan error, 1)
			go func() {
				result <- querier.New(setter).SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "live", MaxConcurrency: 1})
			}()
			require.Eventually(t, func() bool {
				var waiting bool
				check, e := pgx.Connect(ctx, smokePostgresDSN())
				if e != nil {
					return false
				}
				defer check.Close(ctx)
				e = check.QueryRow(ctx, "SELECT cardinality(pg_blocking_pids($1)) > 0", setter.PgConn().PID()).Scan(&waiting)
				return e == nil && waiting
			}, time.Second, 5*time.Millisecond)
			require.NoError(t, tx.Commit(ctx))
			require.NoError(t, <-result)
			count(t, "SELECT in_use FROM anclax.task_tag_concurrency WHERE tag='live'", 1)
			_, err = port(t).ClaimByID(ctx, enqueue(t, `["live"]`), worker.ClaimRequest{})
			require.ErrorIs(t, err, worker.ErrNoTask)
		})

		t.Run("rejected_candidate_releases_partial_guards", func(t *testing.T) {
			reset(t)
			limit(t, "a", 1)
			limit(t, "z", 1)
			id := enqueue(t, `["a","z"]`)
			blocker, err := conn.Begin(ctx)
			require.NoError(t, err)
			defer blocker.Rollback(ctx)
			_, err = blocker.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('anclax:task-slot:z:1',0))")
			require.NoError(t, err)
			claimant, err := pgx.Connect(ctx, smokePostgresDSN())
			require.NoError(t, err)
			defer claimant.Close(ctx)
			tx, err := claimant.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx)
			var admitted, acquired bool
			require.NoError(t, tx.QueryRow(ctx, "SELECT anclax.try_admit_task_tags($1)", id).Scan(&admitted))
			require.False(t, admitted)
			require.NoError(t, blocker.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended('anclax:task-slot:a:1',0))").Scan(&acquired))
			require.True(t, acquired, "the rejected candidate must not retain its earlier tag guard")
		})

		t.Run("uncommitted_release_does_not_block_another_slot_or_require_wakeup", func(t *testing.T) {
			reset(t)
			limit(t, "shared", 2)
			p := port(t)
			first, err := p.ClaimByID(ctx, enqueue(t, `["shared"]`), worker.ClaimRequest{})
			require.NoError(t, err)
			second := enqueue(t, `["shared"]`)
			third := enqueue(t, `["shared"]`)
			finalizer, err := conn.Begin(ctx)
			require.NoError(t, err)
			defer finalizer.Rollback(ctx)
			_, err = finalizer.Exec(ctx, `UPDATE anclax.tasks SET locked_at=NULL,lease_expires_at=NULL,lease_duration_ms=NULL,status='completed' WHERE id=$1`, first.ID)
			require.NoError(t, err)
			claimCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			task, err := p.ClaimByID(claimCtx, second, worker.ClaimRequest{})
			require.NoError(t, err, "another free slot must remain usable while release holds its updated tuple")
			_, err = p.ClaimByID(ctx, third, worker.ClaimRequest{})
			require.ErrorIs(t, err, worker.ErrNoTask)
			require.NoError(t, finalizer.Commit(ctx))
			next, err := p.ClaimByID(ctx, third, worker.ClaimRequest{})
			require.NoError(t, err, "committed release is usable immediately without maintenance")
			require.NoError(t, p.FinalizeTask(ctx, *task, nil))
			require.NoError(t, p.FinalizeTask(ctx, *next, nil))
			count(t, "SELECT count(*) FROM anclax.task_tag_permits", 0)
		})

		t.Run("batch_honors_strict_cap_labels_tag_limits_and_serial_heads", func(t *testing.T) {
			reset(t)
			limit(t, "limited", 2)
			_, err := conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,priority,serial_key)
				SELECT CASE WHEN n<=10 THEN '{"tags":["limited"]}'::jsonb ELSE '{}'::jsonb END,
				'{"type":"admission-probe"}','pending',CASE WHEN n<=10 THEN 1 ELSE 0 END,
				CASE WHEN n BETWEEN 11 AND 20 THEN 'one-serial' ELSE NULL END FROM generate_series(1,80) n;
				INSERT INTO anclax.tasks(attributes,spec,status,priority) VALUES ('{"labels":["other"]}','{"type":"admission-probe"}','pending',100)`)
			require.NoError(t, err)
			p := port(t)
			prepareReadyFixture(t, ctx, m, nil)
			tasks, err := p.ClaimBatch(ctx, worker.ClaimBatchRequest{BatchSize: 32, StrictSlots: 3, Groups: []string{worker.DefaultWeightGroup}})
			require.NoError(t, err)
			require.Len(t, tasks, 32)
			strict := 0
			for _, task := range tasks {
				if task.Priority > 0 {
					strict++
				}
			}
			require.Equal(t, 2, strict)
			count(t, "SELECT count(*) FROM anclax.tasks WHERE locked_at IS NOT NULL AND serial_key='one-serial'", 1)
			count(t, "SELECT count(*) FROM anclax.tasks WHERE locked_at IS NOT NULL AND attributes ? 'labels'", 0)
			count(t, "SELECT count(*) FROM anclax.tasks WHERE locked_at IS NOT NULL AND spec->>'type'<>'prefetchTasks'", 32)
			for _, task := range tasks {
				require.NoError(t, p.FinalizeTask(ctx, *task, nil))
			}
			count(t, "SELECT count(*) FROM anclax.task_tag_permits", 0)
		})

		t.Run("batch_prefers_weight_group_and_falls_back_within_the_query", func(t *testing.T) {
			reset(t)
			_, err := conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,weight)
				VALUES ('{"labels":["a","b"]}','{"type":"admission-probe"}','pending',1),
				       ('{"labels":["b"]}','{"type":"admission-probe"}','pending',100),
				       ('{}','{"type":"admission-probe"}','pending',1000)`)
			require.NoError(t, err)
			p, err := worker.NewModelPort(m, uuid.New(), []string{"a", "b"}, nil, 9*time.Second, 0)
			require.NoError(t, err)
			prepareReadyFixture(t, ctx, m, []string{"a", "b"})
			tasks, err := p.ClaimBatch(ctx, worker.ClaimBatchRequest{BatchSize: 2,
				Groups: []string{"a", "b", worker.DefaultWeightGroup}, WeightedLabels: []string{"a", "b"}})
			require.NoError(t, err)
			require.Len(t, tasks, 2)
			count(t, "SELECT count(*) FROM anclax.tasks WHERE locked_at IS NOT NULL AND attributes ? 'labels'", 2)
			tasks, err = p.ClaimBatch(ctx, worker.ClaimBatchRequest{BatchSize: 2,
				Groups: []string{"a", "b", worker.DefaultWeightGroup}, WeightedLabels: []string{"a", "b"}})
			require.NoError(t, err)
			require.Len(t, tasks, 1, "an empty preferred group must not hide work in a fallback group")
		})

		t.Run("batch_strict_priority_ties_keep_creation_order_regardless_of_weight", func(t *testing.T) {
			reset(t)
			limit(t, "strict", 1)
			var first int32
			require.NoError(t, conn.QueryRow(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,priority,weight)
				VALUES ('{"tags":["strict"]}','{"type":"admission-probe"}','pending',1,1) RETURNING id`).Scan(&first))
			_, err := conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,priority,weight)
				VALUES ('{"tags":["strict"]}','{"type":"admission-probe"}','pending',1,100)`)
			require.NoError(t, err)
			prepareReadyFixture(t, ctx, m, nil)
			tasks, err := port(t).ClaimBatch(ctx, worker.ClaimBatchRequest{BatchSize: 2, StrictSlots: 2, Groups: []string{worker.DefaultWeightGroup}})
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			require.Equal(t, first, tasks[0].ID, "weight only orders normal-priority tasks")
		})

		t.Run("concurrent_batches_and_finalizers_drain_without_deadlocks", func(t *testing.T) {
			reset(t)
			limit(t, "global", 24)
			for i := 0; i < 4; i++ {
				limit(t, fmt.Sprintf("group:%d", i), 8)
			}
			_, err := conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
				SELECT jsonb_build_object('tags',jsonb_build_array('global','group:'||(n%4),'ordinary:'||n)),
				'{"type":"admission-probe"}','pending' FROM generate_series(1,320) n`)
			require.NoError(t, err)
			runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			prepare := prepareReadyFixture(t, ctx, m, nil)
			var completed, claims atomic.Int32
			var wg sync.WaitGroup
			errs := make(chan error, 9)
			schedulerDone := make(chan struct{})
			go func() {
				defer close(schedulerDone)
				for completed.Load() < 320 && runCtx.Err() == nil {
					if err := prepare(runCtx); err != nil {
						errs <- err
						return
					}
					time.Sleep(2 * time.Millisecond)
				}
			}()
			for i := 0; i < 8; i++ {
				p := port(t)
				wg.Add(1)
				go func() {
					defer wg.Done()
					for completed.Load() < 320 && runCtx.Err() == nil {
						claims.Add(1)
						tasks, err := p.ClaimBatch(runCtx, worker.ClaimBatchRequest{BatchSize: 32, Groups: []string{worker.DefaultWeightGroup}})
						if err != nil {
							errs <- err
							return
						}
						for _, task := range tasks {
							if err := p.FinalizeTask(runCtx, *task, nil); err != nil {
								errs <- err
								return
							}
							completed.Add(1)
						}
						if len(tasks) == 0 {
							time.Sleep(5 * time.Millisecond)
						}
					}
				}()
			}
			wg.Wait()
			<-schedulerDone
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			require.Equal(t, int32(320), completed.Load())
			count(t, "SELECT count(*) FROM anclax.task_tag_permits", 0)
			count(t, "SELECT count(*) FROM anclax.task_tag_concurrency WHERE in_use<>0", 0)
			count(t, "SELECT count(*) FROM anclax.tasks WHERE attempts<>1", 0)
			t.Logf("8 concurrent batch claimers: %d claims, %d completions", claims.Load(), completed.Load())
		})

		t.Run("finalize_retries_keep_renewing_without_reexecuting_handler", func(t *testing.T) {
			reset(t)
			limit(t, "busy", 1)
			h := &admissionCountingHandler{}
			p, err := worker.NewModelPort(m, uuid.New(), nil, h, 600*time.Millisecond, 100*time.Millisecond)
			require.NoError(t, err)
			task, err := p.ClaimByID(ctx, enqueue(t, `["busy"]`), worker.ClaimRequest{})
			require.NoError(t, err)
			require.NoError(t, p.ExecuteTask(ctx, *task))
			blocker, err := conn.Begin(ctx)
			require.NoError(t, err)
			defer blocker.Rollback(ctx)
			_, err = blocker.Exec(ctx, "SELECT pg_advisory_xact_lock(42424201)")
			require.NoError(t, err)
			injector, err := pgx.Connect(ctx, smokePostgresDSN())
			require.NoError(t, err)
			defer injector.Close(ctx)
			_, err = injector.Exec(ctx, `CREATE FUNCTION anclax.inject_finalize_retry() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
                IF NEW.locked_at IS NULL AND OLD.locked_at IS NOT NULL AND NOT pg_try_advisory_xact_lock(42424201)
                THEN RAISE EXCEPTION 'injected rollback' USING ERRCODE='55P03'; END IF; RETURN NEW; END $$;
                CREATE TRIGGER inject_finalize_retry BEFORE UPDATE OF locked_at ON anclax.tasks FOR EACH ROW EXECUTE FUNCTION anclax.inject_finalize_retry()`)
			require.NoError(t, err)
			defer func() {
				_, e := injector.Exec(ctx, "DROP TRIGGER inject_finalize_retry ON anclax.tasks; DROP FUNCTION anclax.inject_finalize_retry()")
				require.NoError(t, e)
			}()
			result := make(chan error, 1)
			go func() { result <- p.FinalizeTask(ctx, *task, nil) }()
			time.Sleep(900 * time.Millisecond)
			var live bool
			require.NoError(t, blocker.QueryRow(ctx, "SELECT lease_expires_at>statement_timestamp() FROM anclax.tasks WHERE id=$1", task.ID).Scan(&live))
			require.True(t, live, "the finalize retry window outlasts the original TTL")
			require.True(t, p.TaskRuntimesActive([]int32{task.ID}))
			require.NoError(t, blocker.Commit(ctx))
			require.NoError(t, <-result)
			require.Equal(t, int32(1), h.calls.Load())
			count(t, "SELECT count(*) FROM anclax.tasks WHERE status='completed' AND attempts=1", 1)
			count(t, "SELECT count(*) FROM anclax.task_tag_permits", 0)
		})

		t.Run("automatic_worker_fills_100_slots_in_four_queries", func(t *testing.T) {
			reset(t)
			_, err := conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
				SELECT jsonb_build_object('tags',jsonb_build_array('ordinary')),'{"type":"admission-probe"}','pending' FROM generate_series(1,150)`)
			require.NoError(t, err)
			prepareReadyFixture(t, ctx, m, nil)
			gate := make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(gate) }) }
			defer release()
			h := &admissionCountingHandler{gate: gate}
			p, err := worker.NewModelPort(m, uuid.New(), nil, h, 9*time.Second, time.Second)
			require.NoError(t, err)
			observed := &observedBatchPort{ModelPort: p}
			errs := make(chan error, 100)
			r := worker.NewRuntime(worker.NewEngine(worker.EngineConfig{Concurrency: 100, ClaimBatchSize: 32}), observed,
				worker.RuntimeOptions{PollInterval: 20 * time.Millisecond, OnError: func(err error) { errs <- err }})
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { defer close(done); r.Start(runCtx) }()
			defer func() { release(); cancel(); <-done }()
			require.Eventually(t, func() bool { return h.calls.Load() == 100 }, 3*time.Second, 5*time.Millisecond)
			require.Equal(t, int32(4), observed.calls.Load())
			require.Equal(t, int32(1), observed.peak.Load())
			s, ok := r.Snapshot(ctx)
			require.True(t, ok)
			require.Equal(t, 100, s.InFlight)
			release()
			require.Eventually(t, func() bool {
				var n int
				e := conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE status='completed'").Scan(&n)
				return e == nil && n == 150
			}, 5*time.Second, 5*time.Millisecond)
			require.Equal(t, int32(150), h.calls.Load())
			count(t, "SELECT count(*) FROM anclax.tasks WHERE attempts<>1", 0)
			require.Empty(t, errs)
		})
	})
}

type admissionCountingHandler struct {
	calls atomic.Int32
	gate  <-chan struct{}
}

func (h *admissionCountingHandler) HandleTask(ctx context.Context, _ worker.Task) error {
	h.calls.Add(1)
	if h.gate != nil {
		select {
		case <-h.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (*admissionCountingHandler) RegisterTaskHandler(worker.TaskHandler) {}
func (*admissionCountingHandler) OnTaskFailed(context.Context, core.Tx, worker.TaskSpec, int32) error {
	return nil
}

type captureClaimSQL struct {
	pgx.Tx
	sql  string
	args []any
}

func (c *captureClaimSQL) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.sql, c.args = sql, args
	return c.Tx.QueryRow(ctx, sql, args...)
}

func (c *captureClaimSQL) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.sql, c.args = sql, args
	return c.Tx.Query(ctx, sql, args...)
}

type observedBatchPort struct {
	*worker.ModelPort
	calls  atomic.Int32
	active atomic.Int32
	peak   atomic.Int32
}

func (p *observedBatchPort) ClaimBatch(ctx context.Context, req worker.ClaimBatchRequest) ([]*worker.Task, error) {
	p.calls.Add(1)
	n := p.active.Add(1)
	defer p.active.Add(-1)
	for old := p.peak.Load(); n > old && !p.peak.CompareAndSwap(old, n); old = p.peak.Load() {
	}
	return p.ModelPort.ClaimBatch(ctx, req)
}
