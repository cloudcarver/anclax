//go:build smoke

package taskcoree2e_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/config"
	taskstore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type prefetchLoopHandler struct{}

type prefetchCountingModel struct {
	model.ModelInterface
	calls *atomic.Int64
}

func (m *prefetchCountingModel) RunTransactionWithTx(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
	return m.ModelInterface.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		return f(tx, &prefetchCountingModel{ModelInterface: txm, calls: m.calls})
	})
}

func (m *prefetchCountingModel) PrefetchTaskSupply(ctx context.Context, arg querier.PrefetchTaskSupplyParams) (*querier.PrefetchTaskSupplyRow, error) {
	m.calls.Add(1)
	return m.ModelInterface.PrefetchTaskSupply(ctx, arg)
}

func (m *prefetchCountingModel) TaskLeaseQueries() querier.Querier {
	return m.ModelInterface.(interface{ TaskLeaseQueries() querier.Querier }).TaskLeaseQueries()
}

func (prefetchLoopHandler) HandleTask(context.Context, worker.Task) error { return nil }
func (prefetchLoopHandler) RegisterTaskHandler(worker.TaskHandler)        {}
func (prefetchLoopHandler) OnTaskFailed(context.Context, core.Tx, worker.TaskSpec, int32) error {
	return nil
}

func TestReadyTaskLongPrefetchSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		concurrency, strict := 1, 100
		poll, heartbeat, ttl := 5*time.Millisecond, 50*time.Millisecond, 300*time.Millisecond
		var prefetchCalls atomic.Int64
		observed := &prefetchCountingModel{ModelInterface: m, calls: &prefetchCalls}
		components, err := worker.BuildWorkerComponents(&config.Config{Worker: config.Worker{
			Concurrency: &concurrency, MaxStrictPercentage: &strict, PollInterval: &poll,
			HeartbeatInterval: &heartbeat, LockRefreshInterval: &heartbeat, LockTTL: &ttl,
		}}, observed, prefetchLoopHandler{})
		require.NoError(t, err)
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); components.Runtime.Start(runCtx) }()
		defer func() {
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("long prefetch did not stop")
			}
		}()
		unique := "anclax:system:prefetch"
		var first *querier.AnclaxTask
		require.Eventually(t, func() bool {
			first, err = m.GetTaskByUniqueTag(ctx, &unique)
			return err == nil && first.Status == "running"
		}, 3*time.Second, 5*time.Millisecond)
		require.Equal(t, int32(1), first.Attempts)

		for round := 0; round < 2; round++ {
			// The same scheduler attempt must accept work after an idle period
			// longer than its TTL, while a separate control task still progresses.
			beforeIdle := prefetchCalls.Load()
			time.Sleep(2 * ttl)
			require.LessOrEqual(t, prefetchCalls.Load()-beforeIdle, int64(10), "idle admission backs off while the lease keeper continues")
			var businessID, controlID int32
			require.NoError(t, conn.QueryRow(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
				VALUES('{}','{"type":"long-prefetch-probe"}','pending') RETURNING id`).Scan(&businessID))
			require.NoError(t, conn.QueryRow(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
				VALUES('{}','{"type":"cancelTaskOnWorker"}','pending') RETURNING id`).Scan(&controlID))
			require.Eventually(t, func() bool {
				var completed int
				return conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE id=ANY($1) AND status='completed'", []int32{businessID, controlID}).Scan(&completed) == nil && completed == 2
			}, 3*time.Second, 5*time.Millisecond)
			current, err := m.GetTaskByUniqueTag(ctx, &unique)
			require.NoError(t, err)
			require.Equal(t, "running", current.Status)
			require.Equal(t, first.LeaseVersion, current.LeaseVersion)
			require.Equal(t, first.Attempts, current.Attempts)
			require.True(t, current.LeaseExpiresAt.After(*first.LeaseExpiresAt), "idle scheduler execution renews its lease")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("scheduler shutdown blocked")
		}
		stopped, err := m.GetTaskByUniqueTag(ctx, &unique)
		require.NoError(t, err)
		require.Equal(t, "pending", stopped.Status)
		require.Nil(t, stopped.LockedAt)
		require.Zero(t, stopped.Attempts, "graceful interruption makes the durable job runnable again")

		// Without a keeper, a stale scheduler must still exit via the SQL fence
		// instead of treating lost ownership as an empty queue indefinitely.
		_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET started_at=NULL WHERE id=$1", stopped.ID)
		require.NoError(t, err)
		port, err := worker.NewModelPort(m, uuid.New(), nil, nil, time.Second, 0)
		require.NoError(t, err)
		scheduler, err := port.ClaimControl(ctx, worker.ClaimRequest{})
		require.NoError(t, err)
		owned, err := m.GetTaskByID(ctx, scheduler.ID)
		require.NoError(t, err)
		_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET lease_version=lease_version+1 WHERE id=$1", scheduler.ID)
		require.NoError(t, err)
		fenceCtx, fenceCancel := context.WithTimeout(ctx, time.Second)
		defer fenceCancel()
		err = worker.RunTaskPrefetch(fenceCtx, m, *scheduler, owned.WorkerID.UUID, time.Second)
		require.ErrorIs(t, err, taskstore.ErrTaskLockLost)
	})
}

func TestReadyTaskBulkAdmissionSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		prepare := prepareReadyFixture(t, ctx, m, nil)
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "bulk:limited", MaxConcurrency: 1}))
		_, err = conn.Exec(ctx, `CREATE TABLE anclax.ready_statement_probe(rows_admitted int NOT NULL);
			CREATE FUNCTION anclax.probe_ready_statement() RETURNS trigger LANGUAGE plpgsql AS $$
			DECLARE n int;
			BEGIN
			 SELECT count(*) INTO n FROM updated_rows WHERE status='ready' AND spec->>'type'='bulk-probe';
			 IF n>0 THEN INSERT INTO anclax.ready_statement_probe VALUES(n); END IF;
			 RETURN NULL;
			END; $$;
			CREATE TRIGGER probe_ready_statement AFTER UPDATE ON anclax.tasks
			REFERENCING NEW TABLE AS updated_rows FOR EACH STATEMENT EXECUTE FUNCTION anclax.probe_ready_statement();
			INSERT INTO anclax.tasks(attributes,spec,status)
			 SELECT '{"tags":["bulk:unlimited"]}','{"type":"bulk-probe"}','pending' FROM generate_series(1,100);
			INSERT INTO anclax.tasks(attributes,spec,status)
			 SELECT '{"tags":["bulk:limited"]}','{"type":"limited-probe"}','pending' FROM generate_series(1,2);
			INSERT INTO anclax.tasks(attributes,spec,status,serial_key)
			 SELECT '{}','{"type":"serial-probe"}','pending','bulk:serial' FROM generate_series(1,2);`)
		require.NoError(t, err)
		require.NoError(t, prepare(ctx))
		var statements, admitted, ready, attempts int
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*),COALESCE(sum(rows_admitted),0) FROM anclax.ready_statement_probe").Scan(&statements, &admitted))
		require.Equal(t, 1, statements, "simple candidates enter ready in one UPDATE statement")
		require.Equal(t, 100, admitted)
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*),sum(attempts) FROM anclax.tasks WHERE status='ready'").Scan(&ready, &attempts))
		require.Equal(t, 102, ready, "mixed batch still respects limited and serial admission")
		require.Zero(t, attempts)
		var snapshots int
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE status='ready' AND spec->>'type'='bulk-probe' AND lease_tags=ARRAY['bulk:unlimited'] AND lease_version=1").Scan(&snapshots))
		require.Equal(t, 100, snapshots)
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "bulk:unlimited", MaxConcurrency: 1}))
		usage, err := m.GetTaskTagConcurrency(ctx, "bulk:unlimited")
		require.NoError(t, err)
		require.Equal(t, int32(100), usage.InUse, "bulk reservations retain the snapshots needed for later quota activation")
		_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET status='cancelled' WHERE status='ready'")
		require.NoError(t, err)
		usage, err = m.GetTaskTagConcurrency(ctx, "bulk:unlimited")
		require.NoError(t, err)
		require.Zero(t, usage.InUse)

		for _, isolation := range []pgx.TxIsoLevel{pgx.RepeatableRead, pgx.Serializable} {
			tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
			require.NoError(t, err)
			_, err = tx.Exec(ctx, "SELECT anclax.prefetch_ready_tasks(0,$1,0,256,9000)", uuid.New())
			var pgerr *pgconn.PgError
			require.ErrorAs(t, err, &pgerr)
			require.Equal(t, "0A000", pgerr.Code, "bulk admission must reject stale-snapshot isolation too")
			require.NoError(t, tx.Rollback(ctx))
		}
	})
}

func TestReadyTaskExpiredLeaseBypassesBulkSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		prepare := prepareReadyFixture(t, ctx, m, nil)
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "old:limited", MaxConcurrency: 1}))
		var id int32
		require.NoError(t, conn.QueryRow(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
			VALUES('{"tags":["old:limited"]}','{"type":"expired-bulk-probe"}','pending') RETURNING id`).Scan(&id))
		port, err := worker.NewModelPort(m, uuid.New(), nil, nil, time.Minute, 0)
		require.NoError(t, err)
		old, err := port.ClaimByID(ctx, id, worker.ClaimRequest{})
		require.NoError(t, err)
		_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET attributes='{}',lease_expires_at=statement_timestamp()-interval '1 second' WHERE id=$1", id)
		require.NoError(t, err)
		// Exhaust the bounded maintenance prefix so this old owner reaches the
		// candidate loop with a lease and must release its previous tag allocation.
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,locked_at,lease_expires_at,started_at)
			SELECT '{}','{"type":"maintenance-prefix"}','pending',statement_timestamp()-interval '10 seconds',
			 statement_timestamp()-interval '5 seconds',statement_timestamp()+interval '1 hour' FROM generate_series(1,64)`)
		require.NoError(t, err)
		require.NoError(t, prepare(ctx))
		r, err := m.GetTaskByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "ready", r.Status)
		require.Greater(t, r.LeaseVersion, old.LeaseVersion)
		require.Empty(t, r.LeaseTags)
		usage, err := m.GetTaskTagConcurrency(ctx, "old:limited")
		require.NoError(t, err)
		require.Zero(t, usage.InUse)
	})
}
