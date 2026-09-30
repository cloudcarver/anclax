//go:build smoke

package taskcoree2e_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type prefetchSmallQuotaHandler struct{ prefetchLoopHandler }

func (prefetchSmallQuotaHandler) HandleTask(ctx context.Context, _ worker.Task) error {
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func TestReadyTaskSupplySmallQuotaSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "single-slot", MaxConcurrency: 1}))
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
			SELECT '{"tags":["single-slot"]}','{"type":"small-quota"}','pending' FROM generate_series(1,100)`)
		require.NoError(t, err)
		concurrency := 100
		poll := 5 * time.Millisecond
		components, err := worker.BuildWorkerComponents(&config.Config{Worker: config.Worker{
			Concurrency: &concurrency, PollInterval: &poll,
		}}, m, prefetchSmallQuotaHandler{})
		require.NoError(t, err)
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); components.Runtime.Start(runCtx) }()
		defer func() { cancel(); <-done }()
		require.Eventually(t, func() bool {
			var completed int
			return conn.QueryRow(ctx, `SELECT count(*) FROM anclax.tasks WHERE spec->>'type'='small-quota' AND status='completed'`).Scan(&completed) == nil && completed == 100
		}, 6*time.Second, 20*time.Millisecond, "100 Worker slots must not stretch one-slot quota admission to 250 ms per task")
		usage, err := m.GetTaskTagConcurrency(ctx, "single-slot")
		require.NoError(t, err)
		require.Zero(t, usage.InUse)
	})
}

func TestReadyTaskSupplyDecisionsSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		prepareReadyFixture(t, ctx, m, nil)
		tag := "anclax:system:prefetch"
		sys, err := m.GetTaskByUniqueTag(ctx, &tag)
		require.NoError(t, err)
		supply := func(want int32, reason string) {
			t.Helper()
			r, err := m.PrefetchTaskSupply(ctx, querier.PrefetchTaskSupplyParams{
				TaskID: sys.ID, WorkerID: sys.WorkerID.UUID, LeaseVersion: sys.LeaseVersion,
				BatchSize: 256, LockTtlMs: 9000,
			})
			require.NoError(t, err)
			require.Equal(t, want, r.Prepared)
			require.Equal(t, reason, r.WaitReason)
			var groups map[int64]int32
			require.NoError(t, json.Unmarshal(r.PreparedGroups, &groups))
			var sum int32
			for _, n := range groups {
				sum += n
			}
			require.Equal(t, max(int32(0), want), sum, "committed per-group counts must account for every admitted task")
		}
		supply(0, "idle") // The scheduler's own lease must not make this busy.
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,started_at)
            VALUES('{}','{"type":"future-supply"}','pending',statement_timestamp()+interval '1 hour')`)
		require.NoError(t, err)
		supply(0, "idle")
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "supply:quota", MaxConcurrency: 0}))
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
            SELECT '{"tags":["supply:quota"]}','{"type":"supply-probe"}','pending' FROM generate_series(1,2)`)
		require.NoError(t, err)
		supply(0, "quiescent") // A permanently full zero quota must not spin.
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "supply:quota", MaxConcurrency: 1}))
		supply(1, "productive")
		supply(0, "blocked") // Ready reservations also hold resources.
		port, err := worker.NewModelPort(m, sys.WorkerID.UUID, nil, nil, 9*time.Second, 0)
		require.NoError(t, err)
		tasks, err := port.ClaimBatch(ctx, worker.ClaimBatchRequest{BatchSize: 1, Groups: []string{"__default__"}})
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		supply(0, "blocked")
		supply(0, "blocked") // No new claims: completion can still free a slot.
		require.NoError(t, port.FinalizeTask(ctx, *tasks[0], nil))
		supply(1, "productive")
		require.NoError(t, m.ConfigureWorkerPrefetch(ctx, querier.ConfigureWorkerPrefetchParams{
			WorkerID: sys.WorkerID.UUID, Capacity: 1, StrictPercentage: 100,
		}))
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
            SELECT '{}','{"type":"beyond-worker-capacity"}','pending' FROM generate_series(1,2)`)
		require.NoError(t, err)
		supply(2, "productive") // Ready supply is no longer capped by Worker capacity.
		_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET lease_version=lease_version+1 WHERE id=$1", sys.ID)
		require.NoError(t, err)
		supply(-1, "lost")
	})
}

func TestReadyTaskSupplyIdleProbePlanSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,started_at)
			SELECT '{}','{"type":"future-supply"}','pending',statement_timestamp()+interval '1 hour' FROM generate_series(1,20000);
			INSERT INTO anclax.tasks(attributes,spec,status)
			SELECT '{}','{"type":"old-supply"}','completed' FROM generate_series(1,100000)`)
		require.NoError(t, err)
		_, err = conn.Exec(ctx, "VACUUM ANALYZE anclax.tasks")
		require.NoError(t, err)
		var plan string
		require.NoError(t, conn.QueryRow(ctx, `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) SELECT EXISTS(
			SELECT 1 FROM anclax.tasks WHERE admission_group_id=(SELECT min(id) FROM anclax.task_admission_groups)
            AND status='pending' AND locked_at IS NULL
			AND NOT anclax.is_system_task(spec->>'type')
			AND COALESCE(started_at,'-infinity'::timestamptz)<=statement_timestamp())`).Scan(&plan))
		require.Contains(t, plan, "idx_tasks_pending_due", "future/history rows must not turn every idle probe into a scan")
		prepareReadyFixture(t, ctx, m, nil)
	})
}

// An empty ready set with a running quota owner must back off, but cannot wait
// for another ready consumption event: only finalization can release this slot.
func TestReadyTaskEmptyBackoffRecoverySmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		prepare := prepareReadyFixture(t, ctx, m, nil)
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "backoff:held", MaxConcurrency: 1}))
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
 SELECT '{"tags":["backoff:held"]}','{"type":"backoff-recovery"}','pending' FROM generate_series(1,2)`)
		require.NoError(t, err)
		require.NoError(t, prepare(ctx))
		tag := "anclax:system:prefetch"
		sys, err := m.GetTaskByUniqueTag(ctx, &tag)
		require.NoError(t, err)
		port, err := worker.NewModelPort(m, sys.WorkerID.UUID, nil, nil, 9*time.Second, 0)
		require.NoError(t, err)
		tasks, err := port.ClaimBatch(ctx, worker.ClaimBatchRequest{BatchSize: 1, Groups: []string{"__default__"}})
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		var calls atomic.Int64
		observed := &prefetchCountingModel{ModelInterface: m, calls: &calls}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- worker.RunTaskPrefetch(runCtx, observed, worker.Task{ID: sys.ID, LeaseVersion: sys.LeaseVersion}, sys.WorkerID.UUID, 9*time.Second)
		}()
		defer func() {
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("prefetch failed to stop during backoff")
			}
		}()
		time.Sleep(800 * time.Millisecond)
		require.GreaterOrEqual(t, calls.Load(), int64(3), "empty output cannot permanently stop demand")
		require.LessOrEqual(t, calls.Load(), int64(20), "observations must not bypass the retry deadline")
		require.NoError(t, port.FinalizeTask(ctx, *tasks[0], nil))
		require.Eventually(t, func() bool {
			var n int
			return conn.QueryRow(ctx, `SELECT count(*) FROM anclax.tasks WHERE status='ready' AND spec->>'type'='backoff-recovery'`).Scan(&n) == nil && n == 1
		}, 500*time.Millisecond, 5*time.Millisecond, "resource release resumes admission without any further consumption")
	})
}
