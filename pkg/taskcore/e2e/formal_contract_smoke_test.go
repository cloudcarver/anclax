//go:build smoke

package taskcoree2e_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Exercise generated ClaimTaskBatch SQL and its triggers against PostgreSQL.
// The parameterized proof abstracts LIMIT/filter/primary-key semantics; this
// matrix checks that the reviewed query actually exposes that contract.
func TestFormalBatchContractSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		require.NoError(t, resetDSTState(ctx, m))
		prepare := prepareReadyFixture(t, ctx, m, []string{"route"})
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{
			Tag: "formal", MaxConcurrency: 8}))
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,priority)
			SELECT jsonb_build_object('tags',jsonb_build_array('formal'),
			  'labels',CASE WHEN n%2=0 THEN '["route"]'::jsonb ELSE '[]'::jsonb END),
			  '{"type":"formal-contract"}','pending',CASE WHEN n<=4 THEN 1 ELSE 0 END
			FROM generate_series(1,8) n`)
		require.NoError(t, err)
		require.NoError(t, prepare(ctx))
		var ready int
		require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM anclax.tasks WHERE status='ready'`).Scan(&ready))
		require.Equal(t, 8, ready, "the fixture must have eligible work in both lanes")

		for _, labels := range [][]string{nil, {"route"}} {
			for batch := 1; batch <= 4; batch++ {
				for strictSlots := 0; strictSlots <= batch; strictSlots++ {
					t.Run(fmt.Sprintf("labels_%d_batch_%d_strict_%d", len(labels), batch, strictSlots), func(t *testing.T) {
						tx, err := conn.Begin(ctx)
						require.NoError(t, err)
						defer tx.Rollback(ctx)
						workerID := uuid.New()
						rows, err := querier.New(tx).ClaimTaskBatch(ctx, querier.ClaimTaskBatchParams{
							WorkerID: uuid.NullUUID{UUID: workerID, Valid: true}, LockTtlMs: 9000,
							BatchSize: int32(batch), StrictSlots: int32(strictSlots), Labels: labels,
							GroupNames: []string{worker.DefaultWeightGroup}})
						require.NoError(t, err)
						require.LessOrEqual(t, len(rows), batch)
						expectedStrict := min(strictSlots, 2+len(labels)*2)
						expectedNormal := min(batch-expectedStrict, 2+len(labels)*2)
						require.Len(t, rows, expectedStrict+expectedNormal, "non-vacuous lane selection")
						strict := 0
						ids := make(map[int32]bool)
						for _, row := range rows {
							require.False(t, ids[row.ID], "duplicate task in one response")
							ids[row.ID] = true
							if row.Priority > 0 {
								strict++
							}
							require.Equal(t, "running", row.Status)
							require.Equal(t, workerID, row.WorkerID.UUID)
							require.Equal(t, int64(1), row.LeaseVersion, "ready claim adopts its epoch")
							require.Equal(t, int32(1), row.Attempts)
							var resourceEpoch int64
							require.NoError(t, tx.QueryRow(ctx, `SELECT lease_version FROM anclax.task_tag_slots WHERE task_id=$1 AND tag='formal'`, row.ID).Scan(&resourceEpoch))
							require.Equal(t, row.LeaseVersion, resourceEpoch, "claim preserves the ready reservation")
						}
						require.Equal(t, expectedStrict, strict)
						require.LessOrEqual(t, strict, strictSlots)
						var resources int
						require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM anclax.task_tag_slots WHERE tag='formal' AND task_id IS NOT NULL`).Scan(&resources))
						require.Equal(t, 8, resources, "claim neither releases nor reallocates tag resources")
						require.NoError(t, tx.Rollback(ctx))
					})
				}
			}
		}
	})
}
