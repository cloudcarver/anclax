//go:build smoke

package taskcoree2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
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

func TestReadyTaskConsumptionSmallQuotaSmoke(t *testing.T) {
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

func TestReadyTaskConsumptionSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		prepare := prepareReadyFixture(t, ctx, m, nil)
		owners := []uuid.UUID{uuid.New(), uuid.New()}
		for _, owner := range owners {
			_, err := m.UpsertWorker(ctx, querier.UpsertWorkerParams{ID: owner, Labels: []byte("[]")})
			require.NoError(t, err)
			require.NoError(t, m.ConfigureWorkerPrefetch(ctx, querier.ConfigureWorkerPrefetchParams{WorkerID: owner, Capacity: 100}))
		}
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
			SELECT '{}','{"type":"consumption-probe"}','pending' FROM generate_series(1,6)`)
		require.NoError(t, err)
		require.NoError(t, prepare(ctx))
		claim := func(q querier.Querier, owner uuid.UUID, size int32) int {
			t.Helper()
			claimCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			tasks, err := q.ClaimTaskBatch(claimCtx, querier.ClaimTaskBatchParams{
				WorkerID: uuid.NullUUID{UUID: owner, Valid: true}, BatchSize: size,
				LockTtlMs: 9000, GroupNames: []string{"__default__"},
			})
			require.NoError(t, err)
			return len(tasks)
		}
		counters := func(wantA, wantB int64) {
			t.Helper()
			rows, err := m.ListWorkerPrefetchConsumption(ctx)
			require.NoError(t, err)
			got := make(map[uuid.UUID]int64)
			for _, row := range rows {
				got[row.ID] = row.PrefetchClaimed
			}
			require.Equal(t, wantA, got[owners[0]])
			require.Equal(t, wantB, got[owners[1]])
		}
		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		require.Equal(t, 2, claim(querier.New(tx), owners[0], 2))
		counters(0, 0) // Uncommitted consumption is invisible to the scheduler.
		require.NoError(t, tx.Rollback(ctx))
		counters(0, 0)

		tx, err = conn.Begin(ctx)
		require.NoError(t, err)
		defer tx.Rollback(ctx)
		require.Equal(t, 1, claim(querier.New(tx), owners[0], 1))
		// A holds both its task and counter row. B must still claim immediately:
		// demand accounting adds no cross-Worker shared write lock.
		require.Equal(t, 1, claim(m, owners[1], 1))
		counters(0, 1)
		require.NoError(t, tx.Commit(ctx))
		counters(1, 1)
		require.Equal(t, 4, claim(m, owners[0], 10))
		counters(5, 1)
		require.Zero(t, claim(m, owners[0], 10))
		counters(5, 1)
		var attempts int
		require.NoError(t, conn.QueryRow(ctx, `SELECT sum(attempts) FROM anclax.tasks WHERE spec->>'type'='consumption-probe'`).Scan(&attempts))
		require.Equal(t, 6, attempts, "rolled-back claims do not count as attempts")
		_, err = conn.Exec(ctx, "UPDATE anclax.workers SET status='offline' WHERE id=$1", owners[0])
		require.NoError(t, err)
		rows, err := m.ListWorkerPrefetchConsumption(ctx)
		require.NoError(t, err)
		for _, row := range rows {
			if row.ID == owners[0] {
				require.Zero(t, row.Capacity)
				require.Equal(t, int64(5), row.PrefetchClaimed, "offline counters remain visible without contributing capacity")
			}
		}
	})
}
