//go:build smoke

package taskcoree2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// Connections is the TOTAL budget: main + renewal + producer + observer.
// Durations and arrival rate are independent of observed completions. A capacity
// miss is a result; it must not throttle arrivals or extend warmup indefinitely.
type schedulingCapacityCase struct {
	HandlerJitter                                float64 // Uniform +/- fraction, deterministic per task ID.
	Name, Mode                                   string
	Connections, RenewalConnections, Concurrency int
	HandlerMS, WarmupSeconds, MeasureSeconds     int
	ArrivalRate                                  float64
}

type schedulingCapacityPool struct {
	Total, Acquired, Idle, Max int32
	Acquires, Empty, Canceled  int64
	WaitSeconds                float64
}

func schedulingPool(p *pgxpool.Stat) schedulingCapacityPool {
	return schedulingCapacityPool{p.TotalConns(), p.AcquiredConns(), p.IdleConns(), p.MaxConns(), p.AcquireCount(), p.EmptyAcquireCount(), p.CanceledAcquireCount(), p.EmptyAcquireWaitTime().Seconds()}
}

type schedulingCapacitySample struct {
	ProcessCPUSeconds                                float64
	Seconds, CollectionMS, DBSeconds, HandlerSeconds float64
	Submitted, Scheduled, Completed, Prepared, Empty int64
	Pending, Ready, Running                          int64
	Handlers, Goroutines                             int
	HeapBytes, SysBytes                              uint64
	Main, Renewal                                    schedulingCapacityPool
	SchedulerErrors                                  map[string]float64
	LeaseErrors, LeaseLost, LeaseCalls, Deadlocks    float64
	SQLCalls, SQLMS                                  float64
}

type schedulingCapacityWindow struct {
	ProcessCPUSeconds                                float64
	Seconds, CompletionsPerSecond, ArrivalsPerSecond float64
	MeanExecuting, DBSeconds, DBMSPerCompletion      float64
	MainWaitSeconds, RenewalWaitSeconds              float64
}

type schedulingCapacityResult struct {
	WarmupSchedulerErrors                                             map[string]float64
	ProcessCPUSeconds                                                 float64
	Case                                                              schedulingCapacityCase
	Revision, Postgres                                                string
	GOMAXPROCS, MainPoolLimit, SeedTasks                              int
	Samples                                                           []schedulingCapacitySample
	Windows                                                           []schedulingCapacityWindow
	Seconds, CompletionsPerSecond, ArrivalsPerSecond                  float64
	MeanExecuting, ExecutionUtilization, DBSeconds, DBMSPerCompletion float64
	ThroughputPerBudgetConnection, ExecutingPerBudgetConnection       float64
	ReadyChange, BacklogChange, ProducerShortfall, Completed          int64
	MainWaitSeconds, RenewalWaitSeconds, ProducerMaxLagMS             float64
	MainMeanAcquired, RenewalMeanAcquired                             float64
	PoolSamples, RunnableBacklogSamples, SupplyGapSamples             int
	MainPeak, RenewalPeak                                             int32
	HeapPeakBytes, SysPeakBytes                                       uint64
	LeaseErrors, LeaseLost, Deadlocks                                 float64
	SchedulerErrors                                                   map[string]float64
	WarmupCompleted, FinalDBCompleted, RepeatedTasks                  int64
	Correct, ProducerKeptUp, TargetReached                            bool
}

// A tiny commit observer counts actual successful finalization, not handler
// returns or claim attempts. It adds no database statement or per-task trace.
type schedulingCapacityModel struct {
	model.ModelInterface
	completed, prepared, empty atomic.Int64
}

func (m *schedulingCapacityModel) RunTransactionWithTx(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
	var observed *schedulingCapacityTx
	err := m.ModelInterface.RunTransactionWithTx(ctx, func(tx core.Tx, tm model.ModelInterface) error {
		observed = &schedulingCapacityTx{Tx: tx}
		return f(observed, tm.SpawnWithTx(observed))
	})
	if err == nil && observed != nil {
		m.completed.Add(observed.completed)
		m.prepared.Add(observed.prepared)
		m.empty.Add(observed.empty)
	}
	return err
}

func (m *schedulingCapacityModel) TaskLeaseQueries() querier.Querier {
	return m.ModelInterface.(interface{ TaskLeaseQueries() querier.Querier }).TaskLeaseQueries()
}

func (m *schedulingCapacityModel) TaskLeasePoolStats() *pgxpool.Stat {
	return m.ModelInterface.(interface{ TaskLeasePoolStats() *pgxpool.Stat }).TaskLeasePoolStats()
}

type schedulingCapacityTx struct {
	core.Tx
	completed, prepared, empty int64
}

func (t *schedulingCapacityTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return &schedulingCapacityRow{Row: t.Tx.QueryRow(ctx, sql, args...), tx: t,
		final: strings.HasPrefix(sql, "-- name: FinalizeTaskAttempt "), prefetch: strings.HasPrefix(sql, "-- name: PrefetchTaskSupply ")}
}

type schedulingCapacityRow struct {
	pgx.Row
	tx              *schedulingCapacityTx
	final, prefetch bool
}

func (r *schedulingCapacityRow) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	if err == nil && r.final {
		if status, ok := dest[0].(*string); ok && *status == "completed" {
			r.tx.completed++
		}
	}
	if err == nil && r.prefetch {
		if n, ok := dest[0].(*int32); ok && *n >= 0 {
			r.tx.prepared += int64(*n)
			if *n == 0 {
				r.tx.empty++
			}
		}
	}
	return err
}

type schedulingCapacityHandler struct {
	admissionBenchHandler
	jitter  float64
	mu      sync.Mutex
	active  int
	area    float64
	changed time.Time
}

func (h *schedulingCapacityHandler) observe(delta int) (int, float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if !h.changed.IsZero() {
		h.area += float64(h.active) * now.Sub(h.changed).Seconds()
	}
	h.active += delta
	h.changed = now
	return h.active, h.area
}

func (h *schedulingCapacityHandler) HandleTask(ctx context.Context, task worker.Task) error {
	h.observe(1)
	defer h.observe(-1)
	timer := time.NewTimer(schedulingTaskDelay(h.delay, h.jitter, task.ID))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func schedulingTaskDelay(base time.Duration, jitter float64, id int32) time.Duration {
	// Multiplicative permutation disperses neighboring task IDs reproducibly;
	// this avoids an accidental synchronized timer wave in the primary scan.
	u := float64(uint32(id)*uint32(2654435761)) / float64(uint64(1)<<32)
	return time.Duration(float64(base) * (1 + jitter*(2*u-1)))
}

func TestSchedulingCapacityDelayDistribution(t *testing.T) {
	var sum time.Duration
	for id := int32(1); id <= 10000; id++ {
		require.Equal(t, time.Second, schedulingTaskDelay(time.Second, 0, id))
		d := schedulingTaskDelay(time.Second, .5, id)
		require.GreaterOrEqual(t, d, 500*time.Millisecond)
		require.Less(t, d, 1500*time.Millisecond)
		sum += d
	}
	require.InDelta(t, float64(time.Second), float64(sum/10000), float64(time.Millisecond), "dispersion must preserve the nominal mean used by the capacity target")
}

func TestTaskSchedulingCapacityBenchmark(t *testing.T) {
	if os.Getenv("ANCLAX_SCHEDULING_CAPACITY") != "1" {
		t.Skip("opt-in sustained automatic scheduling capacity benchmark")
	}
	cases := []schedulingCapacityCase{
		{Name: "near-empty", Mode: "near_empty", Connections: 20, RenewalConnections: 2, Concurrency: 100, HandlerMS: 1000, ArrivalRate: 70, WarmupSeconds: 30, MeasureSeconds: 120},
		{Name: "backlogged", Mode: "backlogged", Connections: 20, RenewalConnections: 2, Concurrency: 100, HandlerMS: 1000, ArrivalRate: 120, WarmupSeconds: 30, MeasureSeconds: 120},
	}
	if raw := os.Getenv("ANCLAX_SCHEDULING_CAPACITY_CASES"); raw != "" {
		require.NoError(t, json.Unmarshal([]byte(raw), &cases))
	}
	require.NotEmpty(t, cases)
	maxConnections := 0
	for _, c := range cases {
		require.Contains(t, []string{"near_empty", "backlogged"}, c.Mode)
		require.Greater(t, c.Connections, c.RenewalConnections+2)
		require.Positive(t, c.RenewalConnections)
		require.Positive(t, c.Concurrency)
		require.Positive(t, c.HandlerMS)
		require.Positive(t, c.ArrivalRate)
		require.GreaterOrEqual(t, c.HandlerJitter, float64(0))
		require.Less(t, c.HandlerJitter, float64(1))
		require.Positive(t, c.WarmupSeconds)
		require.GreaterOrEqual(t, c.MeasureSeconds, 10)
		maxConnections = max(maxConnections, c.Connections)
	}
	ctx := context.Background()
	name := fmt.Sprintf("anclax-scheduling-capacity-%d", os.Getpid())
	image := os.Getenv("ANCLAX_SMOKE_POSTGRES_IMAGE")
	if image == "" {
		image = "postgres:17"
	}
	require.NoError(t, runDocker(t, "run", "-d", "--name", name, "--cpus=2", "--memory=2g", "-e", "POSTGRES_PASSWORD=postgres", "-p", "127.0.0.1::5432", image,
		"-c", "max_connections="+strconv.Itoa(maxConnections+10), "-c", "shared_preload_libraries=pg_stat_statements", "-c", "pg_stat_statements.track=all", "-c", "track_io_timing=on"))
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
	var results []schedulingCapacityResult
	t.Cleanup(func() { writeTagTestReport(t, "scheduling-capacity-benchmark", results) })
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) { results = append(results, runSchedulingCapacity(t, ctx, conn, dsn, name, c)) })
	}
}

func runSchedulingCapacity(t *testing.T, ctx context.Context, conn *pgx.Conn, dsn, container string, c schedulingCapacityCase) schedulingCapacityResult {
	t.Helper()
	poll, heartbeat, ttl, strict, renewal := 20*time.Millisecond, time.Second, 9*time.Second, 0, int32(c.RenewalConnections)
	cfg := &config.Config{Pg: config.Pg{DSN: &dsn}, Worker: config.Worker{Concurrency: &c.Concurrency, PollInterval: &poll, HeartbeatInterval: &heartbeat,
		LockRefreshInterval: &heartbeat, LockTTL: &ttl, MaxStrictPercentage: &strict, LeaseRenewalMaxConnections: &renewal}}
	lib := config.DefaultLibConfig()
	lib.Pg.MaxConnections, lib.Pg.MinConnections = int32(c.Connections-c.RenewalConnections-2), 1
	cm := closer.NewCloserManager()
	defer cm.Close()
	base, err := model.NewModel(cfg, lib, cm)
	require.NoError(t, err)
	require.NoError(t, resetDSTState(ctx, base))
	db := base.(*model.Model)
	observed := &schedulingCapacityModel{ModelInterface: base}
	h := &schedulingCapacityHandler{admissionBenchHandler: admissionBenchHandler{delay: time.Duration(c.HandlerMS) * time.Millisecond}, jitter: c.HandlerJitter}
	components, err := worker.BuildWorkerComponents(cfg, observed, h)
	require.NoError(t, err)
	producer, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer producer.Close(ctx)
	result := schedulingCapacityResult{Case: c, Revision: os.Getenv("ANCLAX_ADMISSION_BENCH_REVISION"), MainPoolLimit: int(lib.Pg.MaxConnections), GOMAXPROCS: runtime.GOMAXPROCS(0)}
	require.NoError(t, conn.QueryRow(ctx, "SHOW server_version").Scan(&result.Postgres))
	if c.Mode == "backlogged" {
		result.SeedTasks = 2 * c.Concurrency
		for n := 0; n < result.SeedTasks; n += 256 {
			_, err = producer.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status) SELECT '{}','{"type":"steady-benchmark"}','pending' FROM generate_series(1,$1::int)`, min(256, result.SeedTasks-n))
			require.NoError(t, err)
		}
	}
	errorsBefore := schedulerErrorCounts()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); components.Runtime.Start(runCtx) }()
	defer func() { cancel(); <-done }()
	start := time.Now()
	var submitted atomic.Int64
	submitted.Store(int64(result.SeedTasks))
	producerCtx, stopProducer := context.WithCancel(ctx)
	type production struct {
		lag float64
		err error
	}
	produced := make(chan production, 1)
	go func() {
		lag, err := produceSchedulingLoad(producerCtx, producer, start, c.ArrivalRate, &submitted, result.SeedTasks)
		produced <- production{lag, err}
	}()
	var producerDone bool
	defer func() {
		stopProducer()
		if !producerDone {
			<-produced
		}
	}()

	snapshot := func() schedulingCapacitySample {
		began := time.Now()
		s := schedulingCapacitySample{SchedulerErrors: schedulerErrorCounts()}
		// Only nonterminal rows are inspected during timed work; completed
		// history is counted once after shutdown to validate the commit counter.
		require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='pending'),count(*) FILTER(WHERE status='ready'),count(*) FILTER(WHERE status='running')
 FROM anclax.tasks WHERE status IN ('pending','ready','running') AND spec->>'type'='steady-benchmark'`).Scan(&s.Pending, &s.Ready, &s.Running))
		s.Completed, s.Prepared, s.Empty = observed.completed.Load(), observed.prepared.Load(), observed.empty.Load()
		s.Submitted = submitted.Load()
		s.Seconds = time.Since(start).Seconds()
		s.Scheduled = int64(result.SeedTasks) + int64(math.Floor(s.Seconds*c.ArrivalRate))
		s.Handlers, s.HandlerSeconds = h.observe(0)
		s.Main, s.Renewal = schedulingPool(db.PoolStats()), schedulingPool(db.TaskLeasePoolStats())
		s.Goroutines = runtime.NumGoroutine()
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		s.HeapBytes, s.SysBytes = mem.HeapAlloc, mem.Sys
		s.LeaseErrors, s.LeaseLost, s.LeaseCalls = metricValue(metrics.TaskLeaseRenewalErrorsTotal), metricValue(metrics.TaskLeaseRenewalLostTotal), metricValue(metrics.TaskLeaseRenewalBatchesTotal)
		require.NoError(t, conn.QueryRow(ctx, "SELECT deadlocks FROM pg_stat_database WHERE datname=current_database()").Scan(&s.Deadlocks))
		require.NoError(t, conn.QueryRow(ctx, "SELECT COALESCE(sum(calls),0),COALESCE(sum(total_exec_time),0) FROM pg_stat_statements WHERE toplevel").Scan(&s.SQLCalls, &s.SQLMS))
		s.DBSeconds = admissionBenchCPU(t, container)
		s.ProcessCPUSeconds = schedulingProcessCPU(t)
		s.CollectionMS = float64(time.Since(began)) / float64(time.Millisecond)
		return s
	}

	// Warmup remains finite even if the target is unattainable. Seed work goes
	// through normal pending -> ready -> claim and is never prefilled by hand.
	warmupEnd := start.Add(time.Duration(c.WarmupSeconds) * time.Second)
	for time.Now().Before(warmupEnd) {
		timer := time.NewTimer(min(time.Second, time.Until(warmupEnd)))
		select {
		case <-timer.C:
		case p := <-produced:
			timer.Stop()
			producerDone = true
			t.Fatalf("producer stopped during warmup: %v", p.err)
		}
	}
	first := snapshot()
	result.WarmupSchedulerErrors = make(map[string]float64)
	for key, n := range first.SchedulerErrors {
		if delta := n - errorsBefore[key]; delta > 0 {
			result.WarmupSchedulerErrors[key] = delta
		}
	}
	result.WarmupCompleted = first.Completed
	result.Samples = append(result.Samples, first)
	end := time.Now().Add(time.Duration(c.MeasureSeconds) * time.Second)
	nextSnapshot := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		timer := time.NewTimer(max(0, min(100*time.Millisecond, time.Until(end), time.Until(nextSnapshot))))
		select {
		case <-timer.C:
		case p := <-produced:
			timer.Stop()
			producerDone = true
			t.Fatalf("producer stopped during measurement: %v", p.err)
		}
		main, lease := db.PoolStats(), db.TaskLeasePoolStats()
		result.MainPeak, result.RenewalPeak = max(result.MainPeak, main.TotalConns()), max(result.RenewalPeak, lease.TotalConns())
		result.MainMeanAcquired += float64(main.AcquiredConns())
		result.RenewalMeanAcquired += float64(lease.AcquiredConns())
		result.PoolSamples++
		if time.Now().Before(nextSnapshot) && time.Now().Before(end) {
			continue
		}
		s := snapshot()
		nextSnapshot = time.Now().Add(5 * time.Second)
		if s.Pending+s.Ready > 0 {
			result.RunnableBacklogSamples++
		}
		// Unconstrained fixture: all pending rows are due and eligible. This
		// is a coarse sampled signal, not an attribution of every idle slot.
		if s.Pending > 0 && s.Ready == 0 && s.Running < int64(c.Concurrency) {
			result.SupplyGapSamples++
		}
		result.Samples = append(result.Samples, s)
		w := schedulingWindow(result.Samples[len(result.Samples)-2], s)
		result.Windows = append(result.Windows, w)
		t.Logf("STEADY case=%s seconds=%.1f completed/s=%.1f executing=%.0f pending=%d ready=%d main=%d/%d lease=%d/%d cpu=%.2f", c.Name, s.Seconds-first.Seconds, w.CompletionsPerSecond, w.MeanExecuting, s.Pending, s.Ready, s.Main.Acquired, s.Main.Total, s.Renewal.Acquired, s.Renewal.Total, w.DBSeconds)
	}
	last := result.Samples[len(result.Samples)-1]
	w := schedulingWindow(first, last)
	result.Seconds, result.CompletionsPerSecond, result.ArrivalsPerSecond = w.Seconds, w.CompletionsPerSecond, w.ArrivalsPerSecond
	result.MeanExecuting, result.DBSeconds, result.DBMSPerCompletion = w.MeanExecuting, w.DBSeconds, w.DBMSPerCompletion
	result.ProcessCPUSeconds = w.ProcessCPUSeconds
	result.MainWaitSeconds, result.RenewalWaitSeconds = w.MainWaitSeconds, w.RenewalWaitSeconds
	result.MainMeanAcquired /= float64(result.PoolSamples)
	result.RenewalMeanAcquired /= float64(result.PoolSamples)
	result.Completed = last.Completed - first.Completed
	result.ExecutionUtilization = w.MeanExecuting / float64(c.Concurrency)
	result.ThroughputPerBudgetConnection = w.CompletionsPerSecond / float64(c.Connections)
	result.ExecutingPerBudgetConnection = w.MeanExecuting / float64(c.Connections)
	result.ReadyChange = last.Ready - first.Ready
	result.BacklogChange = last.Pending + last.Ready - first.Pending - first.Ready
	result.ProducerShortfall = last.Scheduled - last.Submitted
	result.LeaseErrors, result.LeaseLost, result.Deadlocks = last.LeaseErrors-first.LeaseErrors, last.LeaseLost-first.LeaseLost, last.Deadlocks-first.Deadlocks
	result.SchedulerErrors = make(map[string]float64)
	for key, n := range last.SchedulerErrors {
		if delta := n - first.SchedulerErrors[key]; delta > 0 {
			result.SchedulerErrors[key] = delta
		}
	}
	for _, s := range result.Samples {
		result.MainPeak, result.RenewalPeak = max(result.MainPeak, s.Main.Total), max(result.RenewalPeak, s.Renewal.Total)
		result.HeapPeakBytes, result.SysPeakBytes = max(result.HeapPeakBytes, s.HeapBytes), max(result.SysPeakBytes, s.SysBytes)
	}
	stopProducer()
	p := <-produced
	producerDone = true
	result.ProducerMaxLagMS = p.lag
	require.True(t, p.err == nil || errors.Is(p.err, context.Canceled), "producer error: %v", p.err)
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("capacity Worker did not stop")
	}
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='completed'),count(*) FILTER(WHERE attempts>1)
 FROM anclax.tasks WHERE spec->>'type'='steady-benchmark'`).Scan(&result.FinalDBCompleted, &result.RepeatedTasks))
	result.Correct = result.FinalDBCompleted == observed.completed.Load() && result.RepeatedTasks == 0 && result.LeaseErrors == 0 && result.LeaseLost == 0 && result.Deadlocks == 0 && len(result.SchedulerErrors) == 0
	// These are explicit reference thresholds, not correctness assertions or a
	// capacity guarantee. Inspect interval trends, delivery lag and stock change.
	result.ProducerKeptUp = result.ProducerShortfall <= int64(math.Max(256, c.ArrivalRate*.1)) && w.ArrivalsPerSecond >= .98*c.ArrivalRate
	if c.Mode == "backlogged" {
		result.TargetReached = result.ProducerKeptUp && result.ExecutionUtilization >= .9 && w.CompletionsPerSecond >= .9*float64(c.Concurrency)*1000/float64(c.HandlerMS)
	} else {
		result.TargetReached = result.ProducerKeptUp && w.CompletionsPerSecond >= .98*c.ArrivalRate && result.BacklogChange <= int64(math.Max(256, c.ArrivalRate*.1))
	}
	result.TargetReached = result.TargetReached && result.Correct && len(result.WarmupSchedulerErrors) == 0
	t.Logf("CAPACITY case=%s correct=%v target=%v completed/s=%.1f executing=%.0f/%d db_ms/task=%.3f connections=%d", c.Name, result.Correct, result.TargetReached, w.CompletionsPerSecond, w.MeanExecuting, c.Concurrency, w.DBMSPerCompletion, c.Connections)
	if !result.Correct {
		t.Error("capacity run failed commit accounting, attempt, lease, deadlock or scheduler-error checks; see report")
	}
	return result
}

func schedulingWindow(a, b schedulingCapacitySample) schedulingCapacityWindow {
	w := schedulingCapacityWindow{Seconds: b.Seconds - a.Seconds, DBSeconds: b.DBSeconds - a.DBSeconds, MainWaitSeconds: b.Main.WaitSeconds - a.Main.WaitSeconds, RenewalWaitSeconds: b.Renewal.WaitSeconds - a.Renewal.WaitSeconds}
	w.ProcessCPUSeconds = b.ProcessCPUSeconds - a.ProcessCPUSeconds
	w.CompletionsPerSecond = float64(b.Completed-a.Completed) / w.Seconds
	w.ArrivalsPerSecond = float64(b.Submitted-a.Submitted) / w.Seconds
	w.MeanExecuting = (b.HandlerSeconds - a.HandlerSeconds) / w.Seconds
	if b.Completed > a.Completed {
		w.DBMSPerCompletion = w.DBSeconds * 1000 / float64(b.Completed-a.Completed)
	}
	return w
}

func schedulingProcessCPU(t *testing.T) float64 {
	t.Helper()
	var usage syscall.Rusage
	require.NoError(t, syscall.Getrusage(syscall.RUSAGE_SELF, &usage))
	return float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
}

func schedulerErrorCounts() map[string]float64 {
	result := make(map[string]float64)
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		panic(err)
	}
	for _, f := range families {
		if f.GetName() != "anclax_task_scheduler_errors_total" {
			continue
		}
		for _, m := range f.Metric {
			var key string
			for _, l := range m.Label {
				key += l.GetName() + "=" + l.GetValue() + ";"
			}
			result[key] = m.GetCounter().GetValue()
		}
	}
	return result
}

func produceSchedulingLoad(ctx context.Context, conn *pgx.Conn, start time.Time, rate float64, submitted *atomic.Int64, seed int) (float64, error) {
	var sent int64
	var maxLag float64
	for {
		due := int64(math.Floor(time.Since(start).Seconds() * rate))
		if due <= sent {
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return maxLag, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		n := min(int64(256), due-sent)
		_, err := conn.Exec(ctx, `INSERT INTO anclax.tasks(attributes,spec,status,started_at)
 SELECT '{}','{"type":"steady-benchmark"}','pending',to_timestamp($1::double precision+(n+$2::bigint)/$3::double precision)
 FROM generate_series(1,$4::int) n`, float64(start.UnixMicro())/1e6, sent, rate, n)
		if err != nil {
			return maxLag, err
		}
		maxLag = max(maxLag, (time.Since(start).Seconds()-float64(sent+1)/rate)*1000)
		sent += n
		submitted.Store(int64(seed) + sent)
	}
}
