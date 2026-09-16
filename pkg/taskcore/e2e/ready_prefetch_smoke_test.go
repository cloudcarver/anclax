//go:build smoke

package taskcoree2e_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestReadyTaskPrefetchSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		owner := uuid.New()
		_, err = m.UpsertWorker(ctx, querier.UpsertWorkerParams{ID: owner, Labels: []byte("[]")})
		require.NoError(t, err)
		require.NoError(t, m.ConfigureWorkerPrefetch(ctx, querier.ConfigureWorkerPrefetchParams{WorkerID: owner, Capacity: 100, StrictPercentage: 100}))
		require.NoError(t, m.EnsureTaskPrefetch(ctx))
		p, err := worker.NewModelPort(m, owner, nil, nil, 9*time.Second, 0)
		require.NoError(t, err)
		sys, err := p.ClaimControl(ctx, worker.ClaimRequest{})
		require.NoError(t, err)
		prepare := func() int32 {
			n, err := m.PrefetchReadyTasks(ctx, querier.PrefetchReadyTasksParams{TaskID: sys.ID, WorkerID: owner, LeaseVersion: sys.LeaseVersion, BatchSize: 256, LockTtlMs: 9000})
			require.NoError(t, err)
			return n
		}
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "ready:limited", MaxConcurrency: 2}))
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
            SELECT '{"tags":["ready:limited"]}','{"type":"ready-probe"}','pending' FROM generate_series(1,5)`)
		require.NoError(t, err)
		require.Equal(t, int32(2), prepare())
		state, err := m.GetTaskTagConcurrency(ctx, "ready:limited")
		require.NoError(t, err)
		require.Equal(t, int32(2), state.InUse)
		var readyIDs []int32
		rows, err := conn.Query(ctx, `SELECT id FROM anclax.tasks WHERE status='ready' ORDER BY id`)
		require.NoError(t, err)
		for rows.Next() {
			var id int32
			require.NoError(t, rows.Scan(&id))
			readyIDs = append(readyIDs, id)
		}
		rows.Close()
		require.NoError(t, rows.Err())
		reserved, err := m.GetTaskByID(ctx, readyIDs[0])
		require.NoError(t, err)
		require.Zero(t, reserved.Attempts)
		require.Nil(t, reserved.LockedAt)
		got, err := p.ClaimBatch(ctx, worker.ClaimBatchRequest{BatchSize: 1, Groups: []string{"__default__"}})
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, reserved.LeaseVersion, got[0].LeaseVersion)
		require.Equal(t, int32(1), got[0].Attempts)
		current, err := m.GetTaskByID(ctx, got[0].ID)
		require.NoError(t, err)
		require.Equal(t, "running", current.Status)
		require.Equal(t, int32(0), prepare(), "ready and running both consume capacity")
		require.NoError(t, p.FinalizeTask(ctx, *got[0], nil))
		require.Equal(t, int32(1), prepare(), "finalize frees capacity without clearing waiter flags")
		time.Sleep(2100 * time.Millisecond)
		require.Equal(t, int32(0), prepare(), "elapsed time must not revoke ready reservations")
		state, err = m.GetTaskTagConcurrency(ctx, "ready:limited")
		require.NoError(t, err)
		require.Equal(t, int32(2), state.InUse)
		_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET status='cancelled' WHERE status='ready'`)
		require.NoError(t, err)
		state, err = m.GetTaskTagConcurrency(ctx, "ready:limited")
		require.NoError(t, err)
		require.Zero(t, state.InUse)
	})
}

// prepareReadyFixture runs the same durable system admission query used by the
// automatic runtime, while keeping worker claiming under the test's control.
func prepareReadyFixture(t *testing.T, ctx context.Context, m model.ModelInterface, labels []string) func(context.Context) error {
	t.Helper()
	owner := uuid.New()
	raw, err := json.Marshal(labels)
	require.NoError(t, err)
	if labels == nil {
		raw = []byte("[]")
	}
	_, err = m.UpsertWorker(ctx, querier.UpsertWorkerParams{ID: owner, Labels: raw})
	require.NoError(t, err)
	require.NoError(t, m.ConfigureWorkerPrefetch(ctx, querier.ConfigureWorkerPrefetchParams{WorkerID: owner, Capacity: 1000, StrictPercentage: 100}))
	require.NoError(t, m.EnsureTaskPrefetch(ctx))
	p, err := worker.NewModelPort(m, owner, labels, nil, time.Hour, 0)
	require.NoError(t, err)
	sys, err := p.ClaimControl(ctx, worker.ClaimRequest{})
	require.NoError(t, err)
	prepare := func(ctx context.Context) error {
		_, err := m.PrefetchReadyTasks(ctx, querier.PrefetchReadyTasksParams{TaskID: sys.ID, WorkerID: owner, LeaseVersion: sys.LeaseVersion, BatchSize: 256, LockTtlMs: 9000})
		return err
	}
	require.NoError(t, prepare(ctx))
	return prepare
}

func TestReadyTaskTransitionsSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		reset := func(t *testing.T) { require.NoError(t, resetDSTState(ctx, m)) }
		enqueue := func(t *testing.T, attrs, serial string) int32 {
			var id int32
			require.NoError(t, conn.QueryRow(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,serial_key)
                VALUES($1,'{"type":"transition-probe"}','pending',NULLIF($2,'')) RETURNING id`, attrs, serial).Scan(&id))
			return id
		}
		usage := func(t *testing.T, tag string, want int32) {
			r, err := m.GetTaskTagConcurrency(ctx, tag)
			require.NoError(t, err)
			require.Equal(t, want, r.InUse)
		}
		port := func(t *testing.T) *worker.ModelPort {
			p, err := worker.NewModelPort(m, uuid.New(), nil, nil, 9*time.Second, 0)
			require.NoError(t, err)
			return p
		}
		t.Run("serial_ready_owner_blocks_direct_and_prefetch_admission", func(t *testing.T) {
			reset(t)
			first := enqueue(t, `{}`, "serial")
			second := enqueue(t, `{}`, "serial")
			prepare := prepareReadyFixture(t, ctx, m, nil)
			r, err := m.GetTaskByID(ctx, first)
			require.NoError(t, err)
			require.Equal(t, "ready", r.Status)
			p := port(t)
			_, err = p.ClaimByID(ctx, second, worker.ClaimRequest{})
			require.ErrorIs(t, err, worker.ErrNoTask)
			task, err := p.ClaimByID(ctx, first, worker.ClaimRequest{})
			require.NoError(t, err)
			require.Equal(t, r.LeaseVersion, task.LeaseVersion)
			require.NoError(t, prepare(ctx))
			next, err := m.GetTaskByID(ctx, second)
			require.NoError(t, err)
			require.Equal(t, "pending", next.Status)
			require.NoError(t, p.FinalizeTask(ctx, *task, nil))
			require.NoError(t, prepare(ctx))
			next, err = m.GetTaskByID(ctx, second)
			require.NoError(t, err)
			require.Equal(t, "ready", next.Status)
		})
		t.Run("quota_activation_backfills_ready_and_shrink_preserves_owners", func(t *testing.T) {
			reset(t)
			first := enqueue(t, `{"tags":["activate"]}`, "")
			enqueue(t, `{"tags":["activate"]}`, "")
			prepare := prepareReadyFixture(t, ctx, m, nil)
			require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "activate", MaxConcurrency: 1}))
			usage(t, "activate", 2)
			third := enqueue(t, `{"tags":["activate"]}`, "")
			require.NoError(t, prepare(ctx))
			r, err := m.GetTaskByID(ctx, third)
			require.NoError(t, err)
			require.Equal(t, "pending", r.Status)
			require.NoError(t, m.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{ID: first, Status: "paused"}))
			usage(t, "activate", 1)
			p := port(t)
			tasks, err := p.ClaimBatch(ctx, worker.ClaimBatchRequest{BatchSize: 32, Groups: []string{"__default__"}})
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			require.NoError(t, p.FinalizeTask(ctx, *tasks[0], nil))
			usage(t, "activate", 0)
			require.NoError(t, prepare(ctx))
			r, err = m.GetTaskByID(ctx, third)
			require.NoError(t, err)
			require.Equal(t, "ready", r.Status)
		})
		t.Run("ready_attribute_edits_release_old_reservation", func(t *testing.T) {
			reset(t)
			require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "old", MaxConcurrency: 1}))
			id := enqueue(t, `{"tags":["old"]}`, "")
			prepareReadyFixture(t, ctx, m, nil)
			usage(t, "old", 1)
			before, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET attributes='{"tags":["new"]}' WHERE id=$1`, id)
			require.NoError(t, err)
			after, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "pending", after.Status)
			require.Greater(t, after.LeaseVersion, before.LeaseVersion)
			require.Empty(t, after.LeaseTags)
			usage(t, "old", 0)
		})
		t.Run("uncommitted_claim_cannot_be_adopted_twice", func(t *testing.T) {
			reset(t)
			require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "race", MaxConcurrency: 1}))
			id := enqueue(t, `{"tags":["race"]}`, "")
			prepareReadyFixture(t, ctx, m, nil)
			tx, err := conn.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx)
			args := querier.ClaimTaskBatchParams{WorkerID: uuid.NullUUID{UUID: uuid.New(), Valid: true}, BatchSize: 1, LockTtlMs: 9000, GroupNames: []string{"__default__"}}
			tasks, err := querier.New(tx).ClaimTaskBatch(ctx, args)
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			other, err := pgx.Connect(ctx, smokePostgresDSN())
			require.NoError(t, err)
			defer other.Close(ctx)
			args.WorkerID.UUID = uuid.New()
			tasks, err = querier.New(other).ClaimTaskBatch(ctx, args)
			require.NoError(t, err)
			require.Empty(t, tasks)
			require.NoError(t, tx.Commit(ctx))
			tasks, err = querier.New(other).ClaimTaskBatch(ctx, args)
			require.NoError(t, err)
			require.Empty(t, tasks)
			usage(t, "race", 1)
			r, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "running", r.Status)
			require.Equal(t, int32(1), r.Attempts)
		})
		t.Run("opposite_new_group_insertion_orders_do_not_wait", func(t *testing.T) {
			reset(t)
			a, b := "new:"+uuid.NewString(), "new:"+uuid.NewString()
			other, err := pgx.Connect(ctx, smokePostgresDSN())
			require.NoError(t, err)
			defer other.Close(ctx)
			first, err := conn.Begin(ctx)
			require.NoError(t, err)
			defer first.Rollback(ctx)
			second, err := other.Begin(ctx)
			require.NoError(t, err)
			defer second.Rollback(ctx)
			insert := func(tx pgx.Tx, label string) {
				bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				defer cancel()
				_, err := tx.Exec(bounded, `INSERT INTO anclax.tasks(attributes,spec,status)
                    VALUES(jsonb_build_object('labels',jsonb_build_array($1::text)),'{"type":"new-group"}','pending')`, label)
				require.NoError(t, err)
			}
			insert(first, a)
			insert(second, b)
			insert(first, b)
			insert(second, a)
			require.NoError(t, first.Commit(ctx))
			require.NoError(t, second.Commit(ctx))
			var n int
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM anclax.tasks WHERE admission_group_id IS NULL`).Scan(&n))
			require.Equal(t, 2, n)
			prepareReadyFixture(t, ctx, m, []string{a, b})
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM anclax.tasks WHERE spec->>'type'='new-group' AND status='ready'`).Scan(&n))
			require.Equal(t, 4, n)
		})
		t.Run("weighted_groups_progress_under_one_shared_slot", func(t *testing.T) {
			reset(t)
			_, err = conn.Exec(ctx, `INSERT INTO anclax.worker_runtime_configs(payload) VALUES('{"labelWeights":{"default":1,"a":2,"b":1}}')`)
			require.NoError(t, err)
			require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "fair", MaxConcurrency: 1}))
			for i := 0; i < 12; i++ {
				enqueue(t, `{"tags":["fair"]}`, "")
				enqueue(t, `{"tags":["fair"],"labels":["a"]}`, "")
				enqueue(t, `{"tags":["fair"],"labels":["b"]}`, "")
			}
			prepare := prepareReadyFixture(t, ctx, m, []string{"a", "b"})
			p, err := worker.NewModelPort(m, uuid.New(), []string{"a", "b"}, nil, 9*time.Second, 0)
			require.NoError(t, err)
			counts := map[string]int{}
			for i := 0; i < 12; i++ {
				tasks, err := p.ClaimBatch(ctx, worker.ClaimBatchRequest{BatchSize: 32, Groups: []string{"a", "b", "__default__"}, WeightedLabels: []string{"a", "b"}})
				require.NoError(t, err)
				require.Len(t, tasks, 1)
				group := "default"
				if tasks[0].Attributes.Labels != nil && len(*tasks[0].Attributes.Labels) > 0 {
					group = (*tasks[0].Attributes.Labels)[0]
				}
				counts[group]++
				require.NoError(t, p.FinalizeTask(ctx, *tasks[0], nil))
				if i < 11 {
					require.NoError(t, prepare(ctx))
				}
			}
			require.Equal(t, map[string]int{"default": 3, "a": 6, "b": 3}, counts)
		})
		t.Run("pause_resume_fences_execution_without_replacing_snapshot", func(t *testing.T) {
			reset(t)
			id := enqueue(t, `{"tags":["original"]}`, "")
			prepareReadyFixture(t, ctx, m, nil)
			p := port(t)
			_, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			require.NoError(t, err)
			_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET attributes='{"tags":["edited"]}' WHERE id=$1`, id)
			require.NoError(t, err)
			require.NoError(t, m.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{ID: id, Status: "paused"}))
			require.NoError(t, m.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{ID: id, Status: "pending"}))
			r, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, []string{"original"}, r.LeaseTags)
			require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "original", MaxConcurrency: 1}))
			usage(t, "original", 1)
		})
		t.Run("stale_scheduler_cannot_admit_another_batch", func(t *testing.T) {
			reset(t)
			prepare := prepareReadyFixture(t, ctx, m, nil)
			id := enqueue(t, `{}`, "")
			_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET lease_version=lease_version+1 WHERE spec->>'type'='prefetchTasks'`)
			require.NoError(t, err)
			require.NoError(t, prepare(ctx))
			r, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "pending", r.Status)
		})
		t.Run("unlimited_tag_sets_share_group_and_rekey_on_configuration", func(t *testing.T) {
			reset(t)
			_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
                SELECT jsonb_build_object('tags',jsonb_build_array('unlimited:'||n)),'{"type":"group-probe"}','pending' FROM generate_series(1,1000)n`)
			require.NoError(t, err)
			var n int
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(DISTINCT admission_group_id) FROM anclax.tasks`).Scan(&n))
			require.Equal(t, 1, n)
			require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "unlimited:1", MaxConcurrency: 0}))
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(DISTINCT admission_group_id) FROM anclax.tasks`).Scan(&n))
			require.Equal(t, 2, n)
			require.NoError(t, m.RemoveTaskTagConcurrencyLimit(ctx, "unlimited:1"))
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(DISTINCT admission_group_id) FROM anclax.tasks`).Scan(&n))
			require.Equal(t, 1, n)
		})
	})
}
