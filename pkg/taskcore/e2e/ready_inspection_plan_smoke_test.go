//go:build smoke

package taskcoree2e_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudcarver/anclax"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestReadyTaskInspectionPlanSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		migration, err := anclax.Migrations.ReadFile("sql/migrations/0016_ready_task_prefetch.up.sql")
		require.NoError(t, err)
		_, body, found := strings.Cut(string(migration), "CREATE FUNCTION anclax.inspect_task_prefetch(")
		require.True(t, found)
		_, query, found := strings.Cut(body, "    RETURN QUERY\n")
		require.True(t, found)
		query, _, found = strings.Cut(query, ";\nEND;")
		require.True(t, found)
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "probe:blocked", MaxConcurrency: 0}))
		prepare := prepareReadyFixture(t, ctx, m, nil)
		_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
			SELECT '{"tags":["probe:blocked"]}','{"type":"blocked-probe"}','pending' FROM generate_series(1,200000);
			INSERT INTO anclax.tasks(attributes,spec,status)
			SELECT '{}','{"type":"runnable-probe"}','pending' FROM generate_series(1,2000)`)
		require.NoError(t, err)
		require.NoError(t, prepare(ctx))
		_, err = conn.Exec(ctx, "VACUUM ANALYZE anclax.tasks")
		require.NoError(t, err)
		_, err = conn.Exec(ctx, "SET jit=off")
		require.NoError(t, err)
		var raw []byte
		require.NoError(t, conn.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query).Scan(&raw))
		writeTagTestReport(t, "ready-inspection-plan", json.RawMessage(raw))
		var plan []map[string]any
		require.NoError(t, json.Unmarshal(raw, &plan))
		var check func(map[string]any)
		check = func(node map[string]any) {
			if node["Relation Name"] == "tasks" {
				rows, _ := node["Actual Rows"].(float64)
				removed, _ := node["Rows Removed by Filter"].(float64)
				loops, _ := node["Actual Loops"].(float64)
				require.LessOrEqual(t, (rows+removed)*loops, float64(2048), "inspection must not scan the blocked pending backlog: %v", node)
			}
			if children, ok := node["Plans"].([]any); ok {
				for _, child := range children {
					check(child.(map[string]any))
				}
			}
		}
		check(plan[0]["Plan"].(map[string]any))
	})
}
