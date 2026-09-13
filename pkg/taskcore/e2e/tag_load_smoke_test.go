//go:build smoke

package taskcoree2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/taskcore/ctrl"
	"github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func tagTestPositiveInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	require.NoError(t, err, name)
	require.Positive(t, v, name)
	return v
}

func writeTagTestReport(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	t.Logf("%s: %s", name, raw)
	if dir := os.Getenv("ANCLAX_TEST_REPORT_DIR"); dir != "" {
		require.NoError(t, os.MkdirAll(dir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".json"), raw, 0644))
	}
}

type tagLoadResult struct {
	Scenario           string  `json:"scenario"`
	Postgres           string  `json:"postgres"`
	Workers            int     `json:"workers"`
	TagCardinality     int     `json:"tagCardinality"`
	BlockedBacklog     int     `json:"blockedBacklog"`
	ElapsedSeconds     float64 `json:"elapsedSeconds"`
	Completed          int64   `json:"completed"`
	LimitedCompleted   int64   `json:"limitedCompleted"`
	UnlimitedCompleted int64   `json:"unlimitedCompleted"`
	EmptyClaims        int64   `json:"emptyClaims"`
	TasksPerSecond     float64 `json:"tasksPerSecond"`
	ClaimP95Ms         float64 `json:"claimP95Ms"`
	ClaimP99Ms         float64 `json:"claimP99Ms"`
}

// This opt-in load suite reports real database claim latency under a continuously
// replenished queue. It uses broad regression budgets, not production SLAs.
func TestTaskTagConcurrencyLoadSmoke(t *testing.T) {
	seconds := tagTestPositiveInt(t, "ANCLAX_TAG_LOAD_SECONDS", 5)
	workers := tagTestPositiveInt(t, "ANCLAX_TAG_LOAD_WORKERS", 16)
	cardinality := tagTestPositiveInt(t, "ANCLAX_TAG_LOAD_TAGS", 1000)
	backlog := tagTestPositiveInt(t, "ANCLAX_TAG_LOAD_BACKLOG", 20000)
	p99Budget := tagTestPositiveInt(t, "ANCLAX_TAG_MAX_CLAIM_P99_MS", 500)
	throughputFloor := tagTestPositiveInt(t, "ANCLAX_TAG_MIN_TASKS_PER_SECOND", 10)
	var results []tagLoadResult
	t.Cleanup(func() { writeTagTestReport(t, "tag-load", results) })
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		var version string
		require.NoError(t, conn.QueryRow(ctx, "SHOW server_version").Scan(&version))
		for _, scenario := range []string{"untagged", "shared_hot_tags", "many_tags", "mixed_with_blocked_backlog"} {
			t.Run(scenario, func(t *testing.T) {
				require.NoError(t, resetDSTState(ctx, m))
				s := store.NewTaskStore(m)
				control := ctrl.NewWorkerControlPlane(m, nil, s, nil)
				require.NoError(t, control.SetTagConcurrencyLimit(ctx, "load:global", 3))
				for _, tag := range []string{"load:group:0", "load:group:1", "load:group:2"} {
					require.NoError(t, control.SetTagConcurrencyLimit(ctx, tag, 2))
				}
				if scenario == "many_tags" {
					_, err := conn.Exec(ctx, `INSERT INTO anclax.task_tag_concurrency(tag,max_concurrency)
                        SELECT prefix || n, 2 FROM generate_series(0,$1::int-1) AS n
                        CROSS JOIN (VALUES ('load:tenant:'), ('load:resource:')) AS p(prefix)`, cardinality)
					require.NoError(t, err)
				}
				blockedBacklog := 0
				if scenario == "mixed_with_blocked_backlog" {
					blockedBacklog = backlog
					require.NoError(t, control.SetTagConcurrencyLimit(ctx, "load:blocked", 0))
					_, err := conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
                        SELECT '{"tags":["load:blocked"]}', '{"type":"load","payload":{}}', 'pending' FROM generate_series(1,$1::int)`, backlog)
					require.NoError(t, err)
				}
				var sequence atomic.Int64
				push := func(ctx context.Context) error {
					i := sequence.Add(1)
					var tags []string
					switch scenario {
					case "shared_hot_tags":
						tags = []string{"load:global", "load:group:" + strconv.FormatInt(i%3, 10)}
					case "many_tags":
						tags = []string{"load:tenant:" + strconv.FormatInt(i%int64(cardinality), 10), "load:resource:" + strconv.FormatInt((i+1)%int64(cardinality), 10)}
					case "mixed_with_blocked_backlog":
						if i%3 == 0 {
							tags = []string{"load:global", "load:group:" + strconv.FormatInt((i/3)%3, 10)}
						}
					}
					_, err := s.PushTask(ctx, &apigen.Task{Status: apigen.Pending, Spec: apigen.TaskSpec{Type: "load", Payload: []byte("{}")}, Attributes: apigen.TaskAttributes{Tags: &tags}})
					return err
				}
				for i := 0; i < workers*2; i++ {
					require.NoError(t, push(ctx))
				}
				_, err := conn.Exec(ctx, "ANALYZE anclax.tasks")
				require.NoError(t, err)
				runCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second+30*time.Second)
				defer cancel()
				var limited, unlimited, empty atomic.Int64
				var wg sync.WaitGroup
				latencies := make(chan []time.Duration, workers)
				errs := make(chan error, workers)
				start := time.Now()
				stopAt := start.Add(time.Duration(seconds) * time.Second)
				for i := 0; i < workers; i++ {
					p, err := worker.NewModelPort(m, uuid.New(), nil, nil, 30*time.Second, 0)
					require.NoError(t, err)
					wg.Add(1)
					go func() {
						defer wg.Done()
						var samples []time.Duration
						defer func() { latencies <- samples }()
						for time.Now().Before(stopAt) && runCtx.Err() == nil {
							claimCtx, cancelClaim := context.WithTimeout(runCtx, 2*time.Second)
							begin := time.Now()
							task, err := p.ClaimNormalByGroup(claimCtx, worker.ClaimNormalRequest{Group: worker.DefaultWeightGroup})
							elapsed := time.Since(begin)
							cancelClaim()
							if errors.Is(err, worker.ErrNoTask) {
								empty.Add(1)
								time.Sleep(time.Millisecond)
								continue
							}
							if err != nil {
								errs <- err
								return
							}
							samples = append(samples, elapsed)
							if err := p.FinalizeTask(runCtx, *task, nil); err != nil {
								errs <- err
								return
							}
							if task.Attributes.Tags != nil && len(*task.Attributes.Tags) > 0 {
								limited.Add(1)
							} else {
								unlimited.Add(1)
							}
							if err := push(runCtx); err != nil {
								errs <- err
								return
							}
						}
					}()
				}
				wg.Wait()
				elapsed := time.Since(start)
				close(errs)
				for err := range errs {
					require.NoError(t, err)
				}
				close(latencies)
				var samples []time.Duration
				for batch := range latencies {
					samples = append(samples, batch...)
				}
				require.NotEmpty(t, samples)
				sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
				percentile := func(p int) float64 { return float64(samples[(len(samples)*p+99)/100-1]) / float64(time.Millisecond) }
				completed := limited.Load() + unlimited.Load()
				var tagRows int
				require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.task_tag_concurrency").Scan(&tagRows))
				result := tagLoadResult{Scenario: scenario, Postgres: version, Workers: workers, TagCardinality: tagRows, BlockedBacklog: blockedBacklog,
					ElapsedSeconds: elapsed.Seconds(), Completed: completed, LimitedCompleted: limited.Load(), UnlimitedCompleted: unlimited.Load(), EmptyClaims: empty.Load(),
					TasksPerSecond: float64(completed) / elapsed.Seconds(), ClaimP95Ms: percentile(95), ClaimP99Ms: percentile(99)}
				results = append(results, result)
				t.Logf("%s: %.1f tasks/s, claim p95=%.2fms p99=%.2fms", scenario, result.TasksPerSecond, result.ClaimP95Ms, result.ClaimP99Ms)
				require.LessOrEqual(t, result.ClaimP99Ms, float64(p99Budget), "claim latency regression budget")
				require.GreaterOrEqual(t, result.TasksPerSecond, float64(throughputFloor), "throughput regression budget")
				if scenario == "mixed_with_blocked_backlog" {
					require.Positive(t, limited.Load())
					require.Positive(t, unlimited.Load())
					var stillBlocked int
					require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE concurrency_wait_tag='load:blocked' AND attempts=0").Scan(&stillBlocked))
					require.Equal(t, backlog, stillBlocked)
				}
				var permits, inUse int64
				require.NoError(t, conn.QueryRow(ctx, "SELECT (SELECT count(*) FROM anclax.task_tag_permits), COALESCE(sum(in_use),0) FROM anclax.task_tag_concurrency").Scan(&permits, &inUse))
				require.Zero(t, permits)
				require.Zero(t, inUse)
			})
		}
	})
}
