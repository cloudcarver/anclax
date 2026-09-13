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

func TestTaskTagConcurrencyMigrationSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		source, err := iofs.New(anclax.Migrations, "sql/migrations")
		require.NoError(t, err)
		dsn := strings.Replace(smokePostgresDSN(), "postgres://", "pgx5://", 1) + "&x-migrations-table=anchor_migrations"
		migration, err := migrate.NewWithSourceInstance("iofs", source, dsn)
		require.NoError(t, err)
		defer migration.Close()
		require.NoError(t, migration.Migrate(13))
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		ids := map[string]int32{}
		for _, status := range []string{"pending", "paused", "cancelled", "completed", "failed"} {
			var id int32
			err := conn.QueryRow(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,unique_tag,attempts,lease_version)
                VALUES ('{"tags":["legacy","kind:a","legacy"]}', '{"type":"legacy-probe","payload":{"value":42}}', $1, $1, 2, 7) RETURNING id`, status).Scan(&id)
			require.NoError(t, err)
			ids[status] = id
		}
		_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET locked_at = statement_timestamp(), worker_id = $1 WHERE status IN ('pending','paused','cancelled')`, uuid.New())
		require.NoError(t, err)
		require.NoError(t, migration.Up())
		for status, id := range ids {
			row, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, status, row.Status)
			require.Equal(t, int64(7), row.LeaseVersion)
			require.Equal(t, int32(2), row.Attempts)
			require.Equal(t, []string{"legacy", "kind:a", "legacy"}, *row.Attributes.Tags)
			require.JSONEq(t, `{"value":42}`, string(row.Spec.Payload))
		}
		state, err := m.GetTaskTagConcurrency(ctx, "legacy")
		require.NoError(t, err)
		require.Equal(t, int32(3), state.InUse, "all old held leases count, including pause/cancel")
		require.Nil(t, state.MaxConcurrency)
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "legacy", MaxConcurrency: 1}))
		var mappings, permits int
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.task_tags").Scan(&mappings))
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.task_tag_permits").Scan(&permits))
		require.Equal(t, 10, mappings)
		require.Equal(t, 6, permits)
		// The old worker has stopped. Let the legacy TTL rule expire all of
		// its leases; completed payloads and paused/cancelled states survive.
		_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET locked_at = statement_timestamp() - interval '10 seconds' WHERE locked_at IS NOT NULL")
		require.NoError(t, err)
		p, err := worker.NewModelPort(m, uuid.New(), nil, nil, 9*time.Second, 0)
		require.NoError(t, err)
		task, err := p.ClaimByID(ctx, ids["pending"], worker.ClaimRequest{})
		require.NoError(t, err)
		require.Equal(t, int64(8), task.LeaseVersion)
		require.NoError(t, p.FinalizeTask(ctx, *task, nil))
		state, err = m.GetTaskTagConcurrency(ctx, "legacy")
		require.NoError(t, err)
		require.Zero(t, state.InUse)
		for _, status := range []string{"paused", "cancelled", "completed", "failed"} {
			row, err := m.GetTaskByID(ctx, ids[status])
			require.NoError(t, err)
			require.Equal(t, status, row.Status)
			require.Nil(t, row.LockedAt)
		}
		// Downgrade removes only the feature's state; old task data remains.
		require.NoError(t, migration.Migrate(13))
		var rows int
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE spec->'payload' = '{\"value\":42}'::jsonb").Scan(&rows))
		require.Equal(t, 5, rows)
		require.NoError(t, migration.Up())
	})
}
