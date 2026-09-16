//go:build smoke

package taskcoree2e_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	taskstore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestReadyTaskDurableUnboundedSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		prepare := prepareReadyFixture(t, ctx, m, nil)
		_, err = conn.Exec(ctx, `UPDATE anclax.workers SET prefetch_capacity=1,prefetch_strict_percentage=10;
			INSERT INTO anclax.tasks(attributes,spec,status,priority)
			SELECT '{}','{"type":"durable-stock"}','pending',CASE WHEN n%2=0 THEN 1 ELSE 0 END FROM generate_series(1,5000)n`)
		require.NoError(t, err)
		for i := 0; i < 20; i++ {
			require.NoError(t, prepare(ctx))
		}
		var ready, strict int
		require.NoError(t, conn.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE priority>0) FROM anclax.tasks WHERE status='ready'`).Scan(&ready, &strict))
		require.Equal(t, 5000, ready, "ready is not capped by Worker slots or 4096")
		require.Equal(t, 2500, strict, "strict execution allowance must not impose a ready stock limit")
		_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET lease_version=lease_version+1 WHERE spec->>'type'='prefetchTasks'`)
		require.NoError(t, err)
		require.NoError(t, prepare(ctx)) // Old scheduler cannot alter durable ready.
		port, err := worker.NewModelPort(m, uuid.New(), nil, nil, 9*time.Second, 0)
		require.NoError(t, err)
		tasks, err := port.ClaimBatch(ctx, worker.ClaimBatchRequest{BatchSize: 32, StrictSlots: 1, Groups: []string{"__default__"}})
		require.NoError(t, err)
		require.Len(t, tasks, 32)
		strict = 0
		for _, task := range tasks {
			if task.Priority > 0 {
				strict++
			}
			require.Equal(t, int32(1), task.Attempts)
			require.NoError(t, port.FinalizeTask(ctx, *task, nil))
		}
		require.Equal(t, 1, strict, "Worker claim still enforces its execution allowance")
	})
}

func TestReadyTaskAdmissionPauseSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		owner := uuid.New()
		_, err = m.UpsertWorker(ctx, querier.UpsertWorkerParams{ID: owner, Labels: []byte(`["b","c"]`)})
		require.NoError(t, err)
		require.NoError(t, m.ConfigureWorkerPrefetch(ctx, querier.ConfigureWorkerPrefetchParams{WorkerID: owner, Capacity: 1, StrictPercentage: 100}))
		require.NoError(t, m.EnsureTaskPrefetch(ctx))
		port, err := worker.NewModelPort(m, owner, nil, nil, 9*time.Second, 0)
		require.NoError(t, err)
		sys, err := port.ClaimControl(ctx, worker.ClaimRequest{})
		require.NoError(t, err)
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
			SELECT '{}','{"type":"stock-a"}','pending' FROM generate_series(1,600)`)
		require.NoError(t, err)
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "disabled-stock", MaxConcurrency: 0}))
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
            VALUES('{"tags":["disabled-stock"]}','{"type":"disabled-stock"}','pending')`)
		require.NoError(t, err)
		var calls atomic.Int64
		observed := &prefetchCountingModel{ModelInterface: m, calls: &calls}
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- worker.RunTaskPrefetch(runCtx, observed, *sys, owner, 9*time.Second) }()
		defer func() {
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("scheduler did not stop")
			}
		}()
		count := func(kind string) int {
			var n int
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM anclax.tasks WHERE status='ready' AND spec->>'type'=$1`, kind).Scan(&n))
			return n
		}
		require.Eventually(t, func() bool { return count("stock-a") == 256 }, time.Second, 5*time.Millisecond)
		before := calls.Load()
		time.Sleep(200 * time.Millisecond)
		require.Equal(t, before, calls.Load(), "sufficient stock stops candidate SQL despite pending work, including a zero-quota group")
		require.Zero(t, count("disabled-stock"))
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "disabled-stock", MaxConcurrency: 1}))
		require.Eventually(t, func() bool { return count("disabled-stock") == 1 }, time.Second, 5*time.Millisecond, "a quota change resumes admission without any consumption")

		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status) VALUES('{"labels":["b"]}','{"type":"stock-b"}','pending')`)
		require.NoError(t, err)
		require.Eventually(t, func() bool { return count("stock-b") == 1 }, time.Second, 5*time.Millisecond)
		require.Equal(t, 256, count("stock-a"), "the slow stocked group stays paused while another group starts")

		// A group created concurrently may commit unclassified. Maintenance must
		// classify it even when every known group has stopped admission.
		guard, err := conn.Begin(ctx)
		require.NoError(t, err)
		_, err = guard.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('anclax:admission-group-create',0))`)
		require.NoError(t, err)
		other, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer other.Close(ctx)
		_, err = other.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status) VALUES('{"labels":["c"]}','{"type":"stock-c"}','pending')`)
		require.NoError(t, err)
		require.NoError(t, guard.Commit(ctx))
		require.Eventually(t, func() bool { return count("stock-c") == 1 }, time.Second, 5*time.Millisecond)

		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "paused-recovery", MaxConcurrency: 1}))
		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		defer tx.Rollback(ctx)
		var id int32
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
			VALUES('{"tags":["paused-recovery"]}','{"type":"recover-running"}','pending') RETURNING id`).Scan(&id))
		_, err = querier.New(tx).ClaimTaskByID(ctx, querier.ClaimTaskByIDParams{ID: id, WorkerID: uuid.NullUUID{UUID: uuid.New(), Valid: true}, LockTtlMs: 9000})
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `UPDATE anclax.tasks SET lease_expires_at=statement_timestamp()-interval '1 second',started_at=statement_timestamp()+interval '1 hour' WHERE id=$1`, id)
		require.NoError(t, err)
		before = calls.Load()
		require.NoError(t, tx.Commit(ctx))
		require.Eventually(t, func() bool {
			u, err := m.GetTaskTagConcurrency(ctx, "paused-recovery")
			return err == nil && u.InUse == 0
		}, time.Second, 5*time.Millisecond)
		require.Equal(t, before, calls.Load(), "running lease recovery does not require an admission batch")

		tasks, err := port.ClaimBatch(ctx, worker.ClaimBatchRequest{BatchSize: 64, Groups: []string{"__default__"}})
		require.NoError(t, err)
		require.Len(t, tasks, 64)
		for _, task := range tasks {
			require.NoError(t, port.FinalizeTask(ctx, *task, nil))
		}
		require.Eventually(t, func() bool { return count("stock-a") > 256 }, time.Second, 5*time.Millisecond, "observed consumption resumes batches beyond Worker capacity")
		_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET lease_version=lease_version+1 WHERE id=$1`, sys.ID)
		require.NoError(t, err)
		select {
		case err = <-done:
			require.ErrorIs(t, err, taskstore.ErrTaskLockLost)
			done <- err // Let the deferred shutdown drain the completed result.
		case <-time.After(time.Second):
			t.Fatal("paused scheduler failed to observe lost ownership")
		}
		require.Greater(t, count("stock-a"), 256, "scheduler loss does not revoke ready")
	})
}

func TestReadyTaskStrictStockDoesNotMaskNormalSupplySmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		prepare := prepareReadyFixture(t, ctx, m, nil)
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,priority)
			SELECT '{}','{"type":"strict-stock"}','pending',1 FROM generate_series(1,256);
			INSERT INTO anclax.tasks(attributes,spec,status) VALUES('{}','{"type":"normal-after-strict"}','pending')`)
		require.NoError(t, err)
		require.NoError(t, prepare(ctx))
		unique := "anclax:system:prefetch"
		sys, err := m.GetTaskByUniqueTag(ctx, &unique)
		require.NoError(t, err)
		require.NoError(t, m.ConfigureWorkerPrefetch(ctx, querier.ConfigureWorkerPrefetchParams{WorkerID: sys.WorkerID.UUID, Capacity: 1, StrictPercentage: 0}))
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- worker.RunTaskPrefetch(runCtx, m, worker.Task{ID: sys.ID, LeaseVersion: sys.LeaseVersion}, sys.WorkerID.UUID, 9*time.Second)
		}()
		defer func() { cancel(); <-done }()
		require.Eventually(t, func() bool {
			var n int
			return conn.QueryRow(ctx, `SELECT count(*) FROM anclax.tasks WHERE spec->>'type'='normal-after-strict' AND status='ready'`).Scan(&n) == nil && n == 1
		}, time.Second, 5*time.Millisecond, "strict ready stock cannot pause normal admission")
		var strict int
		require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM anclax.tasks WHERE spec->>'type'='strict-stock' AND status='ready'`).Scan(&strict))
		require.Equal(t, 256, strict, "already computed strict work remains ready")
	})
}
