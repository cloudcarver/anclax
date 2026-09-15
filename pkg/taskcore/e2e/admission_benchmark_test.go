//go:build smoke

package taskcoree2e_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// This file intentionally uses APIs present in v1.4.1 and can be copied
// unchanged to that revision for a before/after comparison. BuildWorkerComponents
// selects each revision's real automatic admission path; manual RunTask and
// direct LIMIT 1 loops would miss the batching change.
func TestTaskAdmissionBenchmark(t *testing.T) {
	if os.Getenv("ANCLAX_ADMISSION_BENCH") != "1" {
		t.Skip("opt-in automatic admission benchmark")
	}
	ctx := context.Background()
	image := os.Getenv("ANCLAX_SMOKE_POSTGRES_IMAGE")
	if image == "" {
		image = "postgres:17"
	}
	name := fmt.Sprintf("anclax-admission-bench-%d", os.Getpid())
	require.NoError(t, runDocker(t, "run", "-d", "--name", name, "--cpus=2", "--memory=1g",
		"-e", "POSTGRES_PASSWORD=postgres", "-p", "127.0.0.1::5432", image,
		"-c", "shared_preload_libraries=pg_stat_statements", "-c", "pg_stat_statements.track=all", "-c", "track_io_timing=on"))
	t.Cleanup(func() { _ = runDocker(t, "rm", "-f", name) })
	address, err := exec.Command("docker", "port", name, "5432/tcp").Output()
	require.NoError(t, err)
	dsn := "postgres://postgres:postgres@" + strings.TrimSpace(string(address)) + "/postgres?sslmode=disable"
	require.NoError(t, waitForPostgres(t, dsn, 20*time.Second))
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, "CREATE EXTENSION pg_stat_statements")
	require.NoError(t, err)
	var postgres string
	require.NoError(t, conn.QueryRow(ctx, "SHOW server_version").Scan(&postgres))
	tasks := tagTestPositiveInt(t, "ANCLAX_ADMISSION_BENCH_TASKS", 2000)
	history := tagTestPositiveInt(t, "ANCLAX_ADMISSION_BENCH_HISTORY", 100000)
	delay := time.Duration(tagTestPositiveInt(t, "ANCLAX_ADMISSION_BENCH_HANDLER_MS", 20)) * time.Millisecond
	timeout := time.Duration(tagTestPositiveInt(t, "ANCLAX_ADMISSION_BENCH_TIMEOUT", 120)) * time.Second
	scenarios := os.Getenv("ANCLAX_ADMISSION_BENCH_SCENARIOS")
	if scenarios == "" {
		scenarios = "untagged,unlimited_shared,limited_shared,serial_history"
	}
	levels := capacityLevels(t, "ANCLAX_ADMISSION_BENCH_CONCURRENCY", []int{100, 200})
	var results []admissionBenchResult
	t.Cleanup(func() { writeTagTestReport(t, "admission-benchmark", results) })
	for _, scenario := range strings.Split(scenarios, ",") {
		for _, concurrency := range levels {
			t.Run(fmt.Sprintf("%s/%d", scenario, concurrency), func(t *testing.T) {
				require.Contains(t, []string{"untagged", "unlimited_shared", "limited_shared", "serial_history"}, scenario)
				poll, heartbeat, ttl, strict, leasePool := 20*time.Millisecond, time.Second, 9*time.Second, 0, int32(10)
				cfg := &config.Config{Pg: config.Pg{DSN: &dsn}, Worker: config.Worker{Concurrency: &concurrency,
					PollInterval: &poll, HeartbeatInterval: &heartbeat, LockRefreshInterval: &heartbeat,
					LockTTL: &ttl, MaxStrictPercentage: &strict, LeaseRenewalMaxConnections: &leasePool}}
				lib := config.DefaultLibConfig()
				lib.Pg.MaxConnections, lib.Pg.MinConnections = 50, 1
				cm := closer.NewCloserManager()
				defer cm.Close()
				base, err := model.NewModel(cfg, lib, cm)
				require.NoError(t, err)
				require.NoError(t, resetDSTState(ctx, base))
				if scenario == "limited_shared" {
					require.NoError(t, base.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "bench:global", MaxConcurrency: 32}))
					for n := 0; n < 8; n++ {
						require.NoError(t, base.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: fmt.Sprintf("bench:group:%d", n), MaxConcurrency: 8}))
					}
				}
				historyRows := 0
				if scenario == "serial_history" {
					historyRows = history
					_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,serial_key)
						SELECT '{}','{"type":"benchmark-history"}','completed','serial:'||(n%256) FROM generate_series(1,$1::int) n`, history)
					require.NoError(t, err)
				}
				_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,serial_key)
					SELECT CASE WHEN $2::text IN ('unlimited_shared','limited_shared')
					THEN jsonb_build_object('tags',jsonb_build_array('bench:global','bench:group:'||(n%8))) ELSE '{}'::jsonb END,
					'{"type":"admission-benchmark"}','pending',CASE WHEN $2::text='serial_history' THEN 'serial:'||(n%256) ELSE NULL END
					FROM generate_series(1,$1::int) n`, tasks, scenario)
				require.NoError(t, err)
				_, err = conn.Exec(ctx, "VACUUM ANALYZE anclax.tasks")
				require.NoError(t, err)
				stats := &admissionBenchStats{operations: map[string]*admissionBenchOperation{}}
				observed := &admissionBenchModel{ModelInterface: base, stats: stats}
				components, err := worker.BuildWorkerComponents(cfg, observed, &admissionBenchHandler{delay: delay})
				require.NoError(t, err)
				var schema int
				require.NoError(t, conn.QueryRow(ctx, "SELECT version FROM anchor_migrations").Scan(&schema))
				_, err = conn.Exec(ctx, "SELECT pg_stat_statements_reset()")
				require.NoError(t, err)
				leaseErrors, leaseLost := metricValue(metrics.TaskLeaseRenewalErrorsTotal), metricValue(metrics.TaskLeaseRenewalLostTotal)
				beforePool := base.(*model.Model).PoolStats()
				cpuStart := admissionBenchCPU(t, name)
				start := time.Now()
				runCtx, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				go func() { defer close(done); components.Runtime.Start(runCtx) }()
				defer func() { cancel(); <-done }()
				completed := 0
				var peak int32
				for time.Since(start) < timeout {
					require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE id>$1 AND status='completed'", historyRows).Scan(&completed))
					peak = max(peak, base.(*model.Model).PoolStats().AcquiredConns())
					if completed == tasks {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("benchmark worker failed to stop")
				}
				elapsed := time.Since(start)
				cpuSeconds := admissionBenchCPU(t, name) - cpuStart
				afterPool := base.(*model.Model).PoolStats()
				result := admissionBenchResult{Revision: os.Getenv("ANCLAX_ADMISSION_BENCH_REVISION"), Postgres: postgres, Schema: schema,
					Scenario: scenario, Concurrency: concurrency, Tasks: tasks, History: historyRows, HandlerMs: delay.Milliseconds(), GOMAXPROCS: runtime.GOMAXPROCS(0),
					ElapsedSeconds: elapsed.Seconds(), CPUSeconds: cpuSeconds, PoolPeak: peak,
					PoolWaits: afterPool.EmptyAcquireCount() - beforePool.EmptyAcquireCount(), PoolWaitSeconds: (afterPool.AcquireDuration() - beforePool.AcquireDuration()).Seconds(),
					LeaseErrors: metricValue(metrics.TaskLeaseRenewalErrorsTotal) - leaseErrors, LeaseLost: metricValue(metrics.TaskLeaseRenewalLostTotal) - leaseLost}
				require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='completed'),COALESCE(sum(attempts),0),count(*) FILTER(WHERE attempts>1)
					FROM anclax.tasks WHERE id>$1`, historyRows).Scan(&result.Completed, &result.Attempts, &result.RepeatedTasks))
				result.TasksPerSecond = float64(result.Completed) / result.ElapsedSeconds
				stats.mu.Lock()
				result.Operations = stats.operations
				for _, op := range result.Operations {
					op.summarize()
				}
				stats.mu.Unlock()
				require.NoError(t, conn.QueryRow(ctx, `SELECT COALESCE(sum(calls),0),COALESCE(sum(total_exec_time),0),
					COALESCE(sum(shared_blks_hit),0),COALESCE(sum(shared_blks_read),0)
					FROM pg_stat_statements WHERE toplevel AND query LIKE '-- name: Claim%' AND query NOT LIKE '-- name: ClaimWorkerCommand%'`).Scan(&result.ClaimSQLCalls, &result.ClaimSQLMs, &result.ClaimSharedHits, &result.ClaimSharedReads))
				require.NoError(t, conn.QueryRow(ctx, `SELECT COALESCE(sum(calls),0) FROM pg_stat_statements
					WHERE NOT toplevel AND query LIKE 'UPDATE anclax.task_tag_concurrency SET in_use%'`).Scan(&result.CounterUpdates))
				require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM anclax.task_tag_permits`).Scan(&result.RemainingPermits))
				result.Passed = result.Completed == tasks && result.RepeatedTasks == 0 && result.LeaseErrors == 0 && result.LeaseLost == 0 && result.RemainingPermits == 0
				for _, op := range result.Operations {
					for code := range op.Errors {
						// Busy-guard and serialization retries are expected to be
						// recovered without a second handler attempt. Deadlocks
						// remain a failed acceptance criterion even if recovered.
						if code != "55P03" && code != "40001" {
							result.Passed = false
						}
					}
				}
				results = append(results, result)
				t.Logf("BENCH scenario=%s concurrency=%d completed=%d/%d elapsed=%.3fs throughput=%.1f/s cpu=%.3fs passed=%v", scenario, concurrency, result.Completed, tasks, result.ElapsedSeconds, result.TasksPerSecond, result.CPUSeconds, result.Passed)
				if !result.Passed {
					t.Errorf("workload failed completion, lease, attempt, permit or SQLSTATE acceptance; see JSON report")
				}
			})
		}
	}
}

type admissionBenchResult struct {
	Revision, Postgres, Scenario                                                                     string
	Schema, Concurrency, Tasks, History, GOMAXPROCS, Completed, RepeatedTasks, RemainingPermits      int
	HandlerMs, Attempts, PoolWaits, ClaimSQLCalls, ClaimSharedHits, ClaimSharedReads, CounterUpdates int64
	ElapsedSeconds, TasksPerSecond, CPUSeconds, PoolWaitSeconds, LeaseErrors, LeaseLost, ClaimSQLMs  float64
	PoolPeak                                                                                         int32
	Passed                                                                                           bool
	Operations                                                                                       map[string]*admissionBenchOperation
}
type admissionBenchOperation struct {
	Calls, Empty, Rows  int
	P50Ms, P95Ms, P99Ms float64
	Errors              map[string]int
	latencies           []time.Duration
}

func (o *admissionBenchOperation) summarize() {
	sort.Slice(o.latencies, func(i, j int) bool { return o.latencies[i] < o.latencies[j] })
	percentile := func(n int) float64 {
		return float64(o.latencies[(len(o.latencies)*n+99)/100-1]) / float64(time.Millisecond)
	}
	o.P50Ms, o.P95Ms, o.P99Ms = percentile(50), percentile(95), percentile(99)
}

type admissionBenchStats struct {
	mu         sync.Mutex
	operations map[string]*admissionBenchOperation
}

func (s *admissionBenchStats) record(name string, elapsed time.Duration, rows int, err error) {
	if name == "" || errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.operations[name]
	if op == nil {
		op = &admissionBenchOperation{Errors: map[string]int{}}
		s.operations[name] = op
	}
	op.Calls++
	op.Rows += rows
	op.latencies = append(op.latencies, elapsed)
	if err == nil && rows == 0 {
		op.Empty++
	}
	if err != nil {
		code := "other"
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			code = pgErr.Code
		}
		op.Errors[code]++
	}
}

type admissionBenchModel struct {
	model.ModelInterface
	stats *admissionBenchStats
}

func (m *admissionBenchModel) RunTransactionWithTx(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
	started := time.Now()
	var observed *admissionBenchTx
	err := m.ModelInterface.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		observed = &admissionBenchTx{Tx: tx}
		return f(observed, txm.SpawnWithTx(observed))
	})
	if observed != nil && !errors.Is(ctx.Err(), context.Canceled) {
		m.stats.record(observed.operation, time.Since(started), observed.rows, err)
	}
	return err
}
func (m *admissionBenchModel) TaskLeaseQueries() querier.Querier {
	return m.ModelInterface.(interface{ TaskLeaseQueries() querier.Querier }).TaskLeaseQueries()
}
func (m *admissionBenchModel) TaskLeasePoolStats() *pgxpool.Stat {
	return m.ModelInterface.(interface{ TaskLeasePoolStats() *pgxpool.Stat }).TaskLeasePoolStats()
}

type admissionBenchTx struct {
	core.Tx
	operation string
	rows      int
}

func (t *admissionBenchTx) identify(sql string) {
	fields := strings.Fields(sql)
	if len(fields) < 3 || fields[0] != "--" || fields[1] != "name:" {
		return
	}
	name := fields[2]
	if (strings.HasPrefix(name, "Claim") && name != "ClaimWorkerCommand") || name == "FinalizeTaskAttempt" {
		t.operation = name
	}
}
func (t *admissionBenchTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	t.identify(sql)
	return &admissionBenchRow{Row: t.Tx.QueryRow(ctx, sql, args...), tx: t}
}
func (t *admissionBenchTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	t.identify(sql)
	rows, err := t.Tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &admissionBenchRows{Rows: rows, tx: t}, nil
}

type admissionBenchRow struct {
	pgx.Row
	tx *admissionBenchTx
}

func (r *admissionBenchRow) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	if err == nil {
		r.tx.rows++
	}
	return err
}

type admissionBenchRows struct {
	pgx.Rows
	tx *admissionBenchTx
}

func (r *admissionBenchRows) Next() bool {
	next := r.Rows.Next()
	if next {
		r.tx.rows++
	}
	return next
}

type admissionBenchHandler struct{ delay time.Duration }

func (h *admissionBenchHandler) HandleTask(ctx context.Context, _ worker.Task) error {
	timer := time.NewTimer(h.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*admissionBenchHandler) RegisterTaskHandler(worker.TaskHandler) {}
func (*admissionBenchHandler) OnTaskFailed(context.Context, core.Tx, worker.TaskSpec, int32) error {
	return nil
}
func admissionBenchCPU(t *testing.T, name string) float64 {
	t.Helper()
	out, err := exec.Command("docker", "exec", name, "cat", "/sys/fs/cgroup/cpu.stat").Output()
	require.NoError(t, err)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			n, err := strconv.ParseFloat(fields[1], 64)
			require.NoError(t, err)
			return n / 1e6
		}
	}
	t.Fatal("cgroup CPU usage unavailable")
	return 0
}
