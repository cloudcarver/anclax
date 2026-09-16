//go:build smoke

package taskcoree2e_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudcarver/anclax"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestReadyTaskMigrationSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		source, err := iofs.New(anclax.Migrations, "sql/migrations")
		require.NoError(t, err)
		dsn := strings.Replace(smokePostgresDSN(), "postgres://", "pgx5://", 1) + "&x-migrations-table=anchor_migrations"
		migration, err := migrate.NewWithSourceInstance("iofs", source, dsn)
		require.NoError(t, err)
		defer migration.Close()
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "rollback", MaxConcurrency: 3}))
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
			SELECT '{"tags":["rollback"]}','{"type":"rollback-probe"}','pending' FROM generate_series(1,3)`)
		require.NoError(t, err)
		prepareReadyFixture(t, ctx, m, nil)
		port, err := worker.NewModelPort(m, uuid.New(), nil, nil, time.Minute, 0)
		require.NoError(t, err)
		claimed, err := port.ClaimBatch(ctx, worker.ClaimBatchRequest{BatchSize: 1, Groups: []string{"__default__"}})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		running, err := m.GetTaskByID(ctx, claimed[0].ID)
		require.NoError(t, err)
		var readyID int32
		require.NoError(t, conn.QueryRow(ctx, "SELECT min(id) FROM anclax.tasks WHERE status='ready'").Scan(&readyID))
		ready, err := m.GetTaskByID(ctx, readyID)
		require.NoError(t, err)

		require.NoError(t, migration.Migrate(15))
		var pending, permits, systems int
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE status='pending'").Scan(&pending))
		require.Equal(t, 3, pending)
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.task_tag_permits").Scan(&permits))
		require.Equal(t, 1, permits, "rollback releases only unconsumed reservations")
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE spec->>'type'='prefetchTasks'").Scan(&systems))
		require.Zero(t, systems)
		require.NoError(t, migration.Up())

		after, err := m.GetTaskByID(ctx, running.ID)
		require.NoError(t, err)
		require.Equal(t, "pending", after.Status, "legacy pending lease remains executable after upgrade")
		require.Equal(t, running.LockedAt, after.LockedAt)
		require.Equal(t, running.LeaseVersion, after.LeaseVersion)
		require.Equal(t, running.LeaseTags, after.LeaseTags)
		require.Equal(t, running.Attempts, after.Attempts)
		afterReady, err := m.GetTaskByID(ctx, readyID)
		require.NoError(t, err)
		require.Equal(t, "pending", afterReady.Status)
		require.Nil(t, afterReady.LockedAt)
		require.Nil(t, afterReady.LeaseExpiresAt)
		require.Empty(t, afterReady.LeaseTags)
		require.Greater(t, afterReady.LeaseVersion, ready.LeaseVersion)
		require.Zero(t, afterReady.Attempts)
		require.NoError(t, port.FinalizeTask(ctx, *claimed[0], nil))
		usage, err := m.GetTaskTagConcurrency(ctx, "rollback")
		require.NoError(t, err)
		require.Zero(t, usage.InUse)
		prepareReadyFixture(t, ctx, m, nil)
		usage, err = m.GetTaskTagConcurrency(ctx, "rollback")
		require.NoError(t, err)
		require.Equal(t, int32(2), usage.InUse, "rolled-back reservations can be prepared again")
	})
}
