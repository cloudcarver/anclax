//go:build smoke

package taskcoree2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func runLateWorkerHeartbeatScenario(t *testing.T, ctx context.Context, m model.ModelInterface) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	workerID := uuid.New()
	registered, err := m.UpsertWorker(ctx, querier.UpsertWorkerParams{ID: workerID, Labels: []byte("[]")})
	require.NoError(t, err)
	locker, err := pgx.Connect(ctx, smokePostgresDSN())
	require.NoError(t, err)
	defer locker.Close(context.Background())
	heartbeat, err := pgx.Connect(ctx, smokePostgresDSN())
	require.NoError(t, err)
	defer heartbeat.Close(context.Background())
	_, err = locker.Exec(ctx, `
        CREATE FUNCTION anclax.test_hold_heartbeat() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN PERFORM pg_advisory_xact_lock(19793241); RETURN NULL; END $$;
        CREATE TRIGGER test_hold_heartbeat BEFORE UPDATE OF last_heartbeat ON anclax.workers
            FOR EACH STATEMENT EXECUTE FUNCTION anclax.test_hold_heartbeat();
        SELECT pg_advisory_lock(19793241)`)
	require.NoError(t, err)
	defer func() {
		_, _ = locker.Exec(context.Background(), `DROP TRIGGER test_hold_heartbeat ON anclax.workers; DROP FUNCTION anclax.test_hold_heartbeat()`)
	}()
	// Hold the heartbeat before it touches the worker row, so the offline write
	// can commit first even though the heartbeat query has already been sent.
	pid := heartbeat.PgConn().PID()
	done := make(chan struct{})
	var heartbeatErr error
	go func() {
		_, heartbeatErr = querier.New(heartbeat).UpdateWorkerHeartbeat(ctx, workerID)
		close(done)
	}()
	defer func() {
		_, _ = locker.Exec(context.Background(), "SELECT pg_advisory_unlock(19793241)")
		<-done
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := locker.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)", pid).Scan(&waiting)
		return err == nil && waiting
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, m.MarkWorkerOffline(ctx, workerID))
	_, err = locker.Exec(ctx, "SELECT pg_advisory_unlock(19793241)")
	require.NoError(t, err)
	<-done
	require.ErrorIs(t, heartbeatErr, pgx.ErrNoRows, "a delayed heartbeat must not revive an offline worker")
	var status string
	var lastHeartbeat time.Time
	require.NoError(t, locker.QueryRow(ctx, "SELECT status,last_heartbeat FROM anclax.workers WHERE id=$1", workerID).Scan(&status, &lastHeartbeat))
	require.Equal(t, "offline", status)
	require.True(t, registered.LastHeartbeat.Equal(lastHeartbeat))
	// Explicit startup registration still permits reusing a configured worker ID.
	_, err = m.UpsertWorker(ctx, querier.UpsertWorkerParams{ID: workerID, Labels: []byte("[]")})
	require.NoError(t, err)
	row, err := m.UpdateWorkerHeartbeat(ctx, workerID)
	require.NoError(t, err)
	require.Equal(t, "online", row.Status)
}
