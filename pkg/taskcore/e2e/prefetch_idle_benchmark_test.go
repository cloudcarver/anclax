//go:build smoke

package taskcoree2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Keep this harness identical in both worktrees. Idle CPU includes ordinary
// Worker polling and renewal as well as prefetch and its consumption reads.
func TestTaskPrefetchIdleBenchmark(t *testing.T) {
	if os.Getenv("ANCLAX_ADMISSION_BENCH") != "1" {
		t.Skip("opt-in idle prefetch benchmark")
	}
	ctx := context.Background()
	name := fmt.Sprintf("anclax-prefetch-idle-%d", os.Getpid())
	require.NoError(t, runDocker(t, "run", "-d", "--name", name, "--cpus=2", "--memory=1g",
		"-e", "POSTGRES_PASSWORD=postgres", "-p", "127.0.0.1::5432", "postgres:17",
		"-c", "shared_preload_libraries=pg_stat_statements", "-c", "pg_stat_statements.track=all"))
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
	concurrency, strict, renewal := 100, 0, int32(10)
	poll, heartbeat, ttl := 20*time.Millisecond, time.Second, 9*time.Second
	cfg := &config.Config{Pg: config.Pg{DSN: &dsn}, Worker: config.Worker{
		Concurrency: &concurrency, MaxStrictPercentage: &strict, PollInterval: &poll,
		HeartbeatInterval: &heartbeat, LockRefreshInterval: &heartbeat, LockTTL: &ttl, LeaseRenewalMaxConnections: &renewal,
	}}
	lib := config.DefaultLibConfig()
	lib.Pg.MaxConnections, lib.Pg.MinConnections = 50, 1
	cm := closer.NewCloserManager()
	defer cm.Close()
	m, err := model.NewModel(cfg, lib, cm)
	require.NoError(t, err)
	components, err := worker.BuildWorkerComponents(cfg, m, &admissionBenchHandler{delay: 20 * time.Millisecond})
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); components.Runtime.Start(runCtx) }()
	defer func() { cancel(); <-done }()
	time.Sleep(2 * time.Second)
	var leaseVersion int64
	require.NoError(t, conn.QueryRow(ctx, `SELECT lease_version FROM anclax.tasks WHERE unique_tag='anclax:system:prefetch' AND status='running'`).Scan(&leaseVersion))
	_, err = conn.Exec(ctx, "SELECT pg_stat_statements_reset()")
	require.NoError(t, err)
	var result struct {
		Revision                                                           string
		ElapsedSeconds, CPUSeconds, TotalSQLMs, FirstCompletionMs, DrainMs float64
		PrefetchCalls, ConsumptionCalls, ClaimCalls                        int64
		Completed, Attempts                                                int
	}
	result.Revision = os.Getenv("ANCLAX_ADMISSION_BENCH_REVISION")
	cpuStart, started := admissionBenchCPU(t, name), time.Now()
	time.Sleep(5 * time.Second)
	result.ElapsedSeconds = time.Since(started).Seconds()
	result.CPUSeconds = admissionBenchCPU(t, name) - cpuStart
	require.NoError(t, conn.QueryRow(ctx, `SELECT
		COALESCE(sum(calls) FILTER(WHERE query LIKE '-- name: PrefetchReadyTasks%'),0),
		COALESCE(sum(calls) FILTER(WHERE query LIKE '-- name: ListWorkerPrefetchConsumption%'),0),
		COALESCE(sum(calls) FILTER(WHERE query LIKE '-- name: ClaimTaskBatch%'),0),
		COALESCE(sum(total_exec_time),0) FROM pg_stat_statements WHERE toplevel`).Scan(
		&result.PrefetchCalls, &result.ConsumptionCalls, &result.ClaimCalls, &result.TotalSQLMs))
	_, err = conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status)
		SELECT '{}','{"type":"idle-benchmark"}','pending' FROM generate_series(1,100)`)
	require.NoError(t, err)
	arrival := time.Now()
	require.Eventually(t, func() bool {
		err := conn.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='completed'),sum(attempts)
			FROM anclax.tasks WHERE spec->>'type'='idle-benchmark'`).Scan(&result.Completed, &result.Attempts)
		if result.Completed > 0 && result.FirstCompletionMs == 0 {
			result.FirstCompletionMs = float64(time.Since(arrival)) / float64(time.Millisecond)
		}
		return err == nil && result.Completed == 100
	}, 3*time.Second, 5*time.Millisecond)
	result.DrainMs = float64(time.Since(arrival)) / float64(time.Millisecond)
	require.Equal(t, 100, result.Attempts)
	var current int64
	require.NoError(t, conn.QueryRow(ctx, `SELECT lease_version FROM anclax.tasks WHERE unique_tag='anclax:system:prefetch' AND status='running'`).Scan(&current))
	require.Equal(t, leaseVersion, current, "same scheduler survives idle and resumes work")
	writeTagTestReport(t, "prefetch-idle-benchmark", result)
}
