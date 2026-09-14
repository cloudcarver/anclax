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

func TestBatchedTaskLeaseRenewalSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		db := m.(*model.Model)
		require.Equal(t, int32(10), db.TaskLeasePoolStats().MaxConns())
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)

		t.Run("versions_statuses_and_expiry_are_fenced_per_task", func(t *testing.T) {
			require.NoError(t, resetDSTState(ctx, m))
			owner := uuid.New()
			otherOwner := uuid.New()
			rows, err := conn.Query(ctx, `INSERT INTO anclax.tasks(spec,attributes,status,worker_id,locked_at,lease_version,lease_duration_ms,lease_expires_at)
				SELECT '{"type":"lease-test","payload":{}}', '{}', 'pending', $1, statement_timestamp(), 3, 9000, statement_timestamp()+interval '9 seconds'
				FROM generate_series(1,6) RETURNING id`, owner)
			require.NoError(t, err)
			var ids []int32
			for rows.Next() {
				var id int32
				require.NoError(t, rows.Scan(&id))
				ids = append(ids, id)
			}
			require.NoError(t, rows.Err())
			rows.Close()
			_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET status='paused' WHERE id=$1", ids[1])
			require.NoError(t, err)
			_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET status='cancelled' WHERE id=$1", ids[2])
			require.NoError(t, err)
			_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET worker_id=$2 WHERE id=$1", ids[3], otherOwner)
			require.NoError(t, err)
			_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET lease_version=4 WHERE id=$1", ids[4])
			require.NoError(t, err)
			_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET lease_expires_at=statement_timestamp()-interval '1 second' WHERE id=$1", ids[5])
			require.NoError(t, err)
			renewed, err := db.TaskLeaseQueries().RefreshTaskLocks(ctx, querier.RefreshTaskLocksParams{
				WorkerID: uuid.NullUUID{UUID: owner, Valid: true}, Ids: ids, LeaseVersions: []int64{3, 3, 3, 3, 3, 3},
			})
			require.NoError(t, err)
			require.Len(t, renewed, 1)
			require.Equal(t, ids[0], renewed[0].ID)
			require.Equal(t, int64(3), renewed[0].LeaseVersion)
		})

		t.Run("renewal_survives_exhausted_business_pool", func(t *testing.T) {
			require.NoError(t, resetDSTState(ctx, m))
			h := newSmokeWorkerHandler()
			p, err := worker.NewModelPort(m, uuid.New(), nil, h, 900*time.Millisecond, 200*time.Millisecond)
			require.NoError(t, err)
			var id int32
			require.NoError(t, conn.QueryRow(ctx, "INSERT INTO anclax.tasks(spec,attributes,status) VALUES ('{\"type\":\"smoke-worker\",\"payload\":{}}','{}','pending') RETURNING id").Scan(&id))
			task, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			require.NoError(t, err)
			executed := make(chan error, 1)
			go func() { executed <- p.ExecuteTask(ctx, *task) }()
			<-h.started()
			defer h.release()
			held := make(chan struct{}, db.PoolStats().MaxConns())
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseAll()
			var wg sync.WaitGroup
			errs := make(chan error, db.PoolStats().MaxConns())
			for i := int32(0); i < db.PoolStats().MaxConns(); i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- m.RunTransaction(ctx, func(model.ModelInterface) error { held <- struct{}{}; <-release; return nil })
				}()
			}
			for i := int32(0); i < db.PoolStats().MaxConns(); i++ {
				<-held
			}
			require.Equal(t, db.PoolStats().MaxConns(), db.PoolStats().AcquiredConns())
			time.Sleep(2200 * time.Millisecond) // More than two lease TTLs.
			select {
			case err := <-executed:
				t.Fatalf("task stopped while renewal pool was available: %v", err)
			default:
			}
			var remaining float64
			require.NoError(t, conn.QueryRow(ctx, "SELECT EXTRACT(EPOCH FROM lease_expires_at-statement_timestamp()) FROM anclax.tasks WHERE id=$1", id).Scan(&remaining))
			require.Positive(t, remaining)
			require.Positive(t, db.TaskLeasePoolStats().AcquireCount())

			// Missing-renewal status diagnosis must also work without business connections.
			_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET status='cancelled' WHERE id=$1", id)
			require.NoError(t, err)
			select {
			case err := <-executed:
				require.ErrorContains(t, err, "cancel")
			case <-time.After(time.Second):
				t.Fatal("cancelled task was not detected through renewal pool")
			}
			releaseAll()
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			require.NoError(t, p.FinalizeTask(ctx, *task, fmt.Errorf("test executor cancelled")))
		})
	})
}
