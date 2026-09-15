//go:build smoke

package taskcoree2e_test

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudcarver/anclax"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestTaskAdmissionMigrationSmoke(t *testing.T) {
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
		require.NoError(t, migration.Migrate(14))
		_, err = conn.Exec(ctx, "INSERT INTO anclax.task_tag_concurrency(tag,max_concurrency) VALUES ('limited',1)")
		require.NoError(t, err)
		var id int32
		require.NoError(t, conn.QueryRow(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
			VALUES ('{"tags":["limited","ordinary"]}','{"type":"migration-probe"}','pending') RETURNING id`).Scan(&id))
		_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET locked_at=statement_timestamp(),
			lease_version=lease_version+1, lease_expires_at=statement_timestamp()+interval '1 minute',lease_duration_ms=60000
			WHERE id=$1 AND anclax.try_admit_task_tags(id)`, id)
		require.NoError(t, err)
		// Preserve the admitted tags even after attribute edits and fencing of
		// the original owner. Pending recovery is still occupied capacity.
		_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET attributes='{"tags":["edited"]}',
			status='paused',lease_version=lease_version+1 WHERE id=$1;
			`, id)
		require.NoError(t, err)
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,locked_at)
			VALUES ('{"tags":["ordinary"]}','{"type":"broadcastCancelTask"}','pending',statement_timestamp())`)
		require.NoError(t, err)
		var waiter int32
		require.NoError(t, conn.QueryRow(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
			VALUES ('{"tags":["ordinary"]}','{"type":"migration-waiter"}','pending') RETURNING id`).Scan(&waiter))
		_, err = conn.Exec(ctx, `UPDATE anclax.tasks SET concurrency_wait_tag='ordinary',
			concurrency_retry_at=statement_timestamp()+interval '100 milliseconds' WHERE id=$1`, waiter)
		require.NoError(t, err)
		require.NoError(t, migration.Up())
		waiting, err := m.GetTaskByID(ctx, waiter)
		require.NoError(t, err)
		require.Equal(t, "pending", waiting.Status)
		var waitColumns int
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema='anclax' AND table_name='tasks' AND column_name LIKE 'concurrency_%'").Scan(&waitColumns))
		require.Zero(t, waitColumns, "v14 wait flags are removed, so old waiters cannot remain parked")
		row, err := m.GetTaskByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, []string{"limited", "ordinary"}, row.LeaseTags)
		require.Equal(t, []string{"edited"}, *row.Attributes.Tags)
		var permits int
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.task_tag_permits").Scan(&permits))
		require.Equal(t, 1, permits, "only the previously limited permit survives migration")
		_, err = m.GetTaskTagConcurrency(ctx, "ordinary")
		require.ErrorIs(t, err, pgx.ErrNoRows)
		require.NoError(t, migration.Migrate(14))
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.task_tag_permits").Scan(&permits))
		require.Equal(t, 2, permits, "rollback rebuilds ordinary admission-time permits and excludes control tasks")
		state, err := m.GetTaskTagConcurrency(ctx, "ordinary")
		require.NoError(t, err)
		require.Equal(t, int32(1), state.InUse)
		require.Nil(t, state.MaxConcurrency)
		_, err = conn.Exec(ctx, "UPDATE anclax.tasks SET locked_at=NULL,lease_expires_at=NULL,lease_duration_ms=NULL,status='completed' WHERE id=$1", id)
		require.NoError(t, err)
		require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.task_tag_permits").Scan(&permits))
		require.Zero(t, permits, "the restored v14 release protocol drains both tags")
		require.NoError(t, migration.Up())
		state, err = m.GetTaskTagConcurrency(ctx, "limited")
		require.NoError(t, err)
		require.Equal(t, int32(1), *state.MaxConcurrency)
		require.Zero(t, state.InUse)
	})
}
