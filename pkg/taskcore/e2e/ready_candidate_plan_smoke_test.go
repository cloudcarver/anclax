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

// Check the production candidate SELECT itself: EXPLAIN of the PL/pgSQL function
// would hide its nested plan. Use row counts, not machine-dependent timing limits.
func TestReadyTaskCandidatePlanSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		migration, err := anclax.Migrations.ReadFile("sql/migrations/0016_ready_task_prefetch.up.sql")
		require.NoError(t, err)
		_, query, found := strings.Cut(string(migration), "    FOR selected_task IN\n")
		require.True(t, found)
		query, _, found = strings.Cut(query, "    LOOP\n")
		require.True(t, found)
		query = "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) " + strings.NewReplacer(
			"p_paused_groups", "ARRAY[]::bigint[]", "p_batch", "256", "p_lock_ttl_ms", "9000", "current_strict", "0", "strict_target", "100",
			"ordered_groups", "ARRAY['__default__']::text[]", "weighted_labels", "ARRAY[]::text[]",
		).Replace(query)
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		var reports []map[string]any
		for _, blocked := range []int{0, 20000, 200000} {
			require.NoError(t, resetDSTState(ctx, m))
			require.NoError(t, m.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "plan:blocked", MaxConcurrency: 0}))
			prepareReadyFixture(t, ctx, m, nil)
			_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,weight)
				SELECT '{"tags":["plan:blocked"]}','{"type":"blocked-plan-probe"}','pending',1000 FROM generate_series(1,$1::int)`, blocked)
			require.NoError(t, err)
			_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
				SELECT '{}','{"type":"plan-probe"}','pending' FROM generate_series(1,2000)`)
			require.NoError(t, err)
			_, err = conn.Exec(ctx, "VACUUM ANALYZE anclax.tasks")
			require.NoError(t, err)
			_, err = conn.Exec(ctx, "SET jit=off")
			require.NoError(t, err)
			_, err = conn.Exec(ctx, "UPDATE anclax.workers SET last_heartbeat=statement_timestamp()")
			require.NoError(t, err)
			var raw []byte
			require.NoError(t, conn.QueryRow(ctx, query).Scan(&raw))
			var plan []map[string]any
			require.NoError(t, json.Unmarshal(raw, &plan))
			root := plan[0]["Plan"].(map[string]any)
			require.Equal(t, float64(256), root["Actual Rows"])
			var check func(map[string]any)
			check = func(node map[string]any) {
				if node["Relation Name"] == "tasks" {
					rows, _ := node["Actual Rows"].(float64)
					removed, _ := node["Rows Removed by Filter"].(float64)
					loops, _ := node["Actual Loops"].(float64)
					require.LessOrEqual(t, (rows+removed)*loops, float64(512),
						"blocked=%d: candidate selection must not revisit the full backlog: %v", blocked, node)
				}
				if children, ok := node["Plans"].([]any); ok {
					for _, child := range children {
						check(child.(map[string]any))
					}
				}
			}
			check(root)
			t.Logf("blocked=%d candidate rows=%v execution_ms=%v shared_hits=%v", blocked, root["Actual Rows"], plan[0]["Execution Time"], root["Shared Hit Blocks"])
			reports = append(reports, map[string]any{"blocked": blocked, "plan": json.RawMessage(raw)})
		}
		writeTagTestReport(t, "ready-candidate-plans", map[string]any{"query": query, "plans": reports})
	})
}
