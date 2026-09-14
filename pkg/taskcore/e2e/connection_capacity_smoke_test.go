//go:build smoke

package taskcoree2e_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/taskcore/listener"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

type asyncCapacityResult struct {
	FirstHandlerError                                          string
	Mode                                                       string
	MainPoolLimit, LeasePoolLimit, Target, Started, Completed  int
	Passed                                                     bool
	Reason                                                     string
	Postgres                                                   string
	ObservationSeconds, RampSeconds, DrainSeconds              float64
	MainPeakConnections, LeasePeakConnections                  int32
	MainPoolWaits, LeasePoolWaits                              int64
	MainPoolWaitSeconds, LeasePoolWaitSeconds                  float64
	LeaseQueries, LeaseErrors, LeaseLosses                     float64
	LeaseQueryMeanMs, LeaseQueryP99UpperMs, MeanLeaseBatchSize float64
	BusinessQueries                                            int
	BusinessQueriesPerSecond, BusinessQueryP99Ms               float64
}

type capacityHandler struct {
	m          model.ModelInterface
	database   bool
	started    chan struct{}
	release    chan struct{}
	measuring  atomic.Bool
	mu         sync.Mutex
	latencies  []time.Duration
	firstError error
}

func (h *capacityHandler) HandleTask(ctx context.Context, task worker.Task) error {
	h.started <- struct{}{}
	if !h.database {
		select {
		case <-h.release:
			return nil
		case <-ctx.Done():
			return h.failed(context.Cause(ctx))
		}
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-h.release:
			return nil
		case <-ctx.Done():
			return h.failed(context.Cause(ctx))
		case <-ticker.C:
			start := time.Now()
			queryCtx, cancel := context.WithTimeout(ctx, time.Second)
			err := h.m.RunTransactionWithTx(queryCtx, func(tx core.Tx, _ model.ModelInterface) error {
				_, err := tx.Exec(queryCtx, "SELECT pg_sleep(0.005)")
				return err
			})
			cancel()
			if h.measuring.Load() {
				h.mu.Lock()
				h.latencies = append(h.latencies, time.Since(start))
				h.mu.Unlock()
			}
			if err != nil {
				return h.failed(err)
			}
		}
	}
}
func (h *capacityHandler) failed(err error) error {
	h.mu.Lock()
	if h.firstError == nil {
		h.firstError = err
	}
	h.mu.Unlock()
	return err
}

func (*capacityHandler) RegisterTaskHandler(worker.TaskHandler) {}
func (*capacityHandler) OnTaskFailed(context.Context, core.Tx, worker.TaskSpec, int32) error {
	return nil
}

func capacityLevels(t *testing.T, env string, fallback []int) []int {
	t.Helper()
	if os.Getenv(env) == "" {
		return fallback
	}
	var levels []int
	for _, raw := range strings.Split(os.Getenv(env), ",") {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		require.NoError(t, err, env)
		require.Positive(t, n, env)
		levels = append(levels, n)
	}
	sort.Ints(levels)
	return levels
}

func metricValue(m prometheus.Metric) float64 {
	var v dto.Metric
	_ = m.Write(&v)
	return v.GetCounter().GetValue()
}
func histogramValue(h prometheus.Histogram) *dto.Histogram {
	var v dto.Metric
	_ = h.Write(&v)
	return v.GetHistogram()
}
func histogramDelta(before, after *dto.Histogram) (mean, p99 float64) {
	count := after.GetSampleCount() - before.GetSampleCount()
	if count == 0 {
		return 0, 0
	}
	mean = (after.GetSampleSum() - before.GetSampleSum()) / float64(count)
	for i, b := range after.Bucket {
		if float64(b.GetCumulativeCount()-before.Bucket[i].GetCumulativeCount()) >= float64(count)*0.99 {
			return mean, b.GetUpperBound()
		}
	}
	return mean, mean
}

// Opt-in capacity search. Tasks are admitted gradually through Runtime.RunTask
// to measure sustained execution rather than an unbounded enqueue/claim burst.
// Idle tasks need only framework I/O; database tasks require four short
// transactions per second, each holding a connection for at least 5 ms.
func TestAsyncTaskConnectionCapacitySmoke(t *testing.T) {
	pools := capacityLevels(t, "ANCLAX_ASYNC_CAPACITY_POOLS", []int{1, 2, 10})
	idleLevels := capacityLevels(t, "ANCLAX_ASYNC_CAPACITY_IDLE_LEVELS", []int{100, 1000, 5000, 10000})
	dbLevels := capacityLevels(t, "ANCLAX_ASYNC_CAPACITY_DB_LEVELS", []int{10, 25, 50, 100, 250, 500, 1000})
	seconds := tagTestPositiveInt(t, "ANCLAX_ASYNC_CAPACITY_SECONDS", 20)
	require.GreaterOrEqual(t, seconds, 20, "observe more than two default lease TTLs")
	modes := os.Getenv("ANCLAX_ASYNC_CAPACITY_MODES")
	if modes == "" {
		modes = "idle,database"
	}
	var results []asyncCapacityResult
	t.Cleanup(func() { writeTagTestReport(t, "async-connection-capacity", results) })
	withSmokePostgres(t, func(ctx context.Context, base model.ModelInterface) {
		conn, err := pgx.Connect(ctx, smokePostgresDSN())
		require.NoError(t, err)
		defer conn.Close(ctx)
		var version string
		require.NoError(t, conn.QueryRow(ctx, "SHOW server_version").Scan(&version))
		for _, mode := range strings.Split(modes, ",") {
			require.Contains(t, []string{"idle", "database"}, mode)
			levels := idleLevels
			if mode == "database" {
				levels = dbLevels
			}
			for _, pool := range pools {
				for _, n := range levels {
					require.NoError(t, resetDSTState(ctx, base))
					result := runAsyncCapacityCase(t, conn, mode, pool, n, seconds)
					result.Postgres = version
					results = append(results, result)
					t.Logf("CAPACITY mode=%s pools=%d+%d tasks=%d started=%d passed=%v reason=%q lease_queries=%.0f lease_errors=%.0f lease_losses=%.0f lease_p99<=%.1fms business_p99=%.1fms",
						mode, pool, pool, n, result.Started, result.Passed, result.Reason, result.LeaseQueries, result.LeaseErrors, result.LeaseLosses, result.LeaseQueryP99UpperMs, result.BusinessQueryP99Ms)
					if !result.Passed {
						break
					} // First failing level brackets capacity.
				}
			}
		}
	})
}

func runAsyncCapacityCase(t *testing.T, conn *pgx.Conn, mode string, pool, n, seconds int) (result asyncCapacityResult) {
	t.Helper()
	result = asyncCapacityResult{Mode: mode, MainPoolLimit: pool, LeasePoolLimit: pool, Target: n}
	dsn := smokePostgresDSN()
	leaseLimit := int32(pool)
	cfg := &config.Config{Pg: config.Pg{DSN: &dsn}, Worker: config.Worker{Concurrency: &n, LeaseRenewalMaxConnections: &leaseLimit}}
	lib := config.DefaultLibConfig()
	lib.Pg.MaxConnections = int32(pool)
	cm := closer.NewCloserManager()
	defer cm.Close()
	m, err := model.NewModel(cfg, lib, cm)
	require.NoError(t, err)
	db := m.(*model.Model)
	require.Equal(t, int32(pool), db.TaskLeasePoolStats().MaxConns())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	h := &capacityHandler{m: m, database: mode == "database", started: make(chan struct{}, n), release: make(chan struct{}, n)}
	c, err := worker.BuildWorkerComponents(cfg, m, h)
	require.NoError(t, err)
	l := listener.NewPollingTaskEventListener(m)
	defer l.Close(context.Background())
	var wg sync.WaitGroup
	out := make(chan error, n)
	pending := 0
	var mainPeak, leasePeak atomic.Int32
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			mainPeak.Store(max(mainPeak.Load(), db.PoolStats().TotalConns()))
			leasePeak.Store(max(leasePeak.Load(), db.TaskLeasePoolStats().TotalConns()))
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() {
		h.measuring.Store(false)
		h.mu.Lock()
		if h.firstError != nil {
			result.FirstHandlerError = h.firstError.Error()
		}
		h.mu.Unlock()
		// Capacity failures also drain gradually, keeping cleanup from creating
		// an unrelated burst of thousands of finalization timeouts.
		cleanupCtx, stopCleanup := context.WithTimeout(context.Background(), 30*time.Second)
	drain:
		for pending > 0 {
			batch := min(pending, 16)
			for i := 0; i < batch; i++ {
				select {
				case h.release <- struct{}{}:
				default:
				}
			}
			for i := 0; i < batch; i++ {
				select {
				case <-out:
					pending--
				case <-cleanupCtx.Done():
					break drain
				}
			}
		}
		stopCleanup()
		cancel()
		c.Runtime.Close()
		wg.Wait()
		<-monitorDone
		result.MainPeakConnections, result.LeasePeakConnections = mainPeak.Load(), leasePeak.Load()
		require.LessOrEqual(t, result.MainPeakConnections, int32(pool))
		require.LessOrEqual(t, result.LeasePeakConnections, int32(pool))
	}()
	rows, err := conn.Query(ctx, `INSERT INTO anclax.tasks(spec,attributes,status)
		SELECT '{"type":"capacity","payload":{}}','{}','pending' FROM generate_series(1,$1::int) RETURNING id`, n)
	require.NoError(t, err)
	var ids []int32
	for rows.Next() {
		var id int32
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	rows.Close()
	var waits []<-chan listener.TaskTerminalEvent
	for _, id := range ids {
		ch, err := l.WaitTask(ctx, id)
		require.NoError(t, err)
		waits = append(waits, ch)
	}
	rampStart := time.Now()
	for _, id := range ids {
		pending++
		wg.Add(1)
		go func(id int32) { defer wg.Done(); out <- c.Runtime.RunTask(ctx, id) }(id)
		select {
		case <-h.started:
			result.Started++
		case err := <-out:
			pending--
			result.Reason = fmt.Sprintf("task exited during admission: %v", err)
			result.RampSeconds = time.Since(rampStart).Seconds()
			return
		case <-ctx.Done():
			result.Reason = "admission timeout"
			return
		}
	}
	result.RampSeconds = time.Since(rampStart).Seconds()
	mainBefore, leaseBefore := db.PoolStats(), db.TaskLeasePoolStats()
	queriesBefore := metricValue(metrics.TaskLeaseRenewalBatchesTotal)
	errorsBefore := metricValue(metrics.TaskLeaseRenewalErrorsTotal)
	lossBefore := metricValue(metrics.TaskLeaseRenewalLostTotal)
	durationBefore := histogramValue(metrics.TaskLeaseRenewalDurationSeconds)
	batchBefore := histogramValue(metrics.TaskLeaseRenewalBatchSize)
	h.measuring.Store(true)
	start := time.Now()
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	select {
	case <-timer.C:
	case err := <-out:
		pending--
		result.Reason = fmt.Sprintf("task exited during observation: %v", err)
	case <-ctx.Done():
		result.Reason = "observation timeout"
	}
	timer.Stop()
	h.measuring.Store(false)
	result.ObservationSeconds = time.Since(start).Seconds()
	mainAfter, leaseAfter := db.PoolStats(), db.TaskLeasePoolStats()
	result.MainPoolWaits = mainAfter.EmptyAcquireCount() - mainBefore.EmptyAcquireCount()
	result.LeasePoolWaits = leaseAfter.EmptyAcquireCount() - leaseBefore.EmptyAcquireCount()
	result.MainPoolWaitSeconds = (mainAfter.EmptyAcquireWaitTime() - mainBefore.EmptyAcquireWaitTime()).Seconds()
	result.LeasePoolWaitSeconds = (leaseAfter.EmptyAcquireWaitTime() - leaseBefore.EmptyAcquireWaitTime()).Seconds()
	result.LeaseQueries = metricValue(metrics.TaskLeaseRenewalBatchesTotal) - queriesBefore
	result.LeaseErrors = metricValue(metrics.TaskLeaseRenewalErrorsTotal) - errorsBefore
	result.LeaseLosses = metricValue(metrics.TaskLeaseRenewalLostTotal) - lossBefore
	mean, p99 := histogramDelta(durationBefore, histogramValue(metrics.TaskLeaseRenewalDurationSeconds))
	result.LeaseQueryMeanMs, result.LeaseQueryP99UpperMs = mean*1000, p99*1000
	result.MeanLeaseBatchSize, _ = histogramDelta(batchBefore, histogramValue(metrics.TaskLeaseRenewalBatchSize))
	h.mu.Lock()
	samples := append([]time.Duration(nil), h.latencies...)
	h.mu.Unlock()
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	result.BusinessQueries = len(samples)
	result.BusinessQueriesPerSecond = float64(len(samples)) / result.ObservationSeconds
	if len(samples) > 0 {
		result.BusinessQueryP99Ms = float64(samples[(len(samples)*99+99)/100-1]) / float64(time.Millisecond)
	}
	if result.LeaseErrors > 0 || result.LeaseLosses > 0 {
		result.Reason = fmt.Sprintf("lease renewal failed: %.0f query errors, %.0f lost leases", result.LeaseErrors, result.LeaseLosses)
	}
	if result.Reason == "" && mode == "database" {
		if result.BusinessQueriesPerSecond < float64(n)*4*0.9 || result.BusinessQueryP99Ms > 250 {
			result.Reason = "business workload missed 90% of required 4 transactions/s/task or p99 exceeded 250ms"
		}
	}
	if result.Reason != "" {
		return
	}
	// Drain in bounded groups so mass finalization is not mistaken for the
	// sustained-execution capacity measured above.
	drainStart := time.Now()
	for remaining := n; remaining > 0; {
		batch := min(remaining, 16)
		for i := 0; i < batch; i++ {
			h.release <- struct{}{}
		}
		for i := 0; i < batch; i++ {
			select {
			case err := <-out:
				pending--
				if err != nil {
					result.Reason = fmt.Sprintf("finalization failed: %v", err)
					return
				}
			case <-ctx.Done():
				result.Reason = "drain timeout"
				return
			}
		}
		remaining -= batch
	}
	result.DrainSeconds = time.Since(drainStart).Seconds()
	waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Second)
	defer waitCancel()
	for _, ch := range waits {
		select {
		case event := <-ch:
			if event.Err != nil || event.Status != apigen.Completed {
				result.Reason = fmt.Sprintf("unexpected terminal event: %+v", event)
				return
			}
		case <-waitCtx.Done():
			result.Reason = "listener did not converge"
			return
		}
	}
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.tasks WHERE status='completed' AND attempts=1").Scan(&result.Completed))
	result.Passed = result.Completed == n
	if !result.Passed {
		result.Reason = "not all tasks completed exactly one attempt"
	}
	return
}
