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
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestTaskLifecycleMigrationSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		source, err := iofs.New(anclax.Migrations, "sql/migrations")
		require.NoError(t, err)
		dsn := strings.Replace(smokePostgresDSN(), "postgres://", "pgx5://", 1) + "&x-migrations-table=anchor_migrations"
		migration, err := migrate.NewWithSourceInstance("iofs", source, dsn)
		require.NoError(t, err)
		defer migration.Close()
		require.NoError(t, migration.Migrate(12))

		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		statuses := []string{"pending", "paused", "completed", "failed", "cancelled"}
		ids := make(map[string]int32, len(statuses))
		for _, status := range statuses {
			var id int32
			err := conn.QueryRow(ctx, `
				insert into anclax.tasks (attributes, spec, status, unique_tag, attempts)
				values ('{}', '{"type":"legacy-probe","payload":{"value":"legacy"}}', $1, $2, 2)
				returning id
			`, status, "legacy-"+status).Scan(&id)
			require.NoError(t, err)
			ids[status] = id
		}
		// Simulate a stopped old worker whose unfinished lease has expired.
		_, err = conn.Exec(ctx, `update anclax.tasks set worker_id = $1, locked_at = statement_timestamp() - interval '10 seconds' where id = $2`, uuid.New(), ids["pending"])
		require.NoError(t, err)
		_, err = conn.Exec(ctx, `insert into anclax.worker_runtime_configs (payload) values ('{"legacy":1}'), ('{"legacy":2}')`)
		require.NoError(t, err)

		require.NoError(t, migration.Up())
		for _, status := range statuses {
			row, err := m.GetTaskByID(ctx, ids[status])
			require.NoError(t, err)
			require.Equal(t, status, row.Status)
			require.Equal(t, int32(2), row.Attempts)
			require.Zero(t, row.LeaseVersion)
			require.Equal(t, "legacy-probe", row.Spec.Type)
			require.JSONEq(t, `{"value":"legacy"}`, string(row.Spec.Payload))
		}
		for version := int64(1); version <= 2; version++ {
			row, err := m.GetWorkerRuntimeConfigByVersion(ctx, version)
			require.NoError(t, err)
			require.Nil(t, row.RequestID)
		}
		latest, err := m.GetLatestWorkerRuntimeConfig(ctx)
		require.NoError(t, err)
		require.Equal(t, int64(2), latest.Version)
		require.JSONEq(t, `{"legacy":2}`, string(latest.Payload))

		p, err := worker.NewModelPort(m, uuid.New(), nil, nil, 9*time.Second, 3*time.Second)
		require.NoError(t, err)
		task, err := p.ClaimByID(ctx, ids["pending"], worker.ClaimRequest{})
		require.NoError(t, err)
		require.Equal(t, int64(1), task.LeaseVersion)
		require.Equal(t, int32(3), task.Attempts)
		require.NoError(t, p.FinalizeTask(ctx, *task, nil))
		row, err := m.GetTaskByID(ctx, task.ID)
		require.NoError(t, err)
		require.Equal(t, "completed", row.Status)
		require.Nil(t, row.LockedAt)
		require.False(t, row.WorkerID.Valid)
	})
}
