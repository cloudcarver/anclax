//go:build smoke
// +build smoke

package taskcoree2e_test

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/taskcore"
	legacyworker "github.com/cloudcarver/anclax/pkg/taskcore/worker"
	workerv2 "github.com/cloudcarver/anclax/pkg/taskcore/workerv2"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type workerImpl string

const (
	implLegacy workerImpl = "legacy"
	implV2     workerImpl = "workerv2"
)

type stressRunConfig struct {
	Impl              workerImpl
	WorkerCount       int
	WorkerConcurrency int
	Tasks             int
	SleepMs           int32
	TaskTimeout       time.Duration
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	LockTTL           time.Duration
	LockRefresh       time.Duration
	LabelsByWorker    [][]string
}

type taskMeta struct {
	EnqueuedAt time.Time
	Group      string
}

type stressMetrics struct {
	Impl              workerImpl
	Workers           int
	WorkerConcurrency int
	TasksTotal        int
	TasksCompleted    int
	TasksFailed       int
	Duration          time.Duration
	ThroughputTPS     float64
	MaxActiveExec     int64
	P50Latency        time.Duration
	P95Latency        time.Duration
	P99Latency        time.Duration
	MaxLatency        time.Duration
	MeanLatency       time.Duration
	GroupCompleted    map[string]int
}

func TestWorkerStressE2E_SingleWorkerConcurrencyRegression(t *testing.T) {
	for _, impl := range []workerImpl{implLegacy, implV2} {
		impl := impl
		t.Run(string(impl), func(t *testing.T) {
			withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
				cfg := stressRunConfig{
					Impl:              impl,
					WorkerCount:       1,
					WorkerConcurrency: 8,
					Tasks:             160,
					SleepMs:           60,
					TaskTimeout:       60 * time.Second,
					PollInterval:      5 * time.Millisecond,
					HeartbeatInterval: 20 * time.Millisecond,
					LockTTL:           3 * time.Second,
					LockRefresh:       200 * time.Millisecond,
					LabelsByWorker:    [][]string{{"w1", "w2"}},
				}

				metrics := runStressE2E(t, ctx, m, cfg)
				logStressMetrics(t, metrics)

				require.Equal(t, cfg.Tasks, metrics.TasksCompleted, "all tasks should complete")
				require.Equal(t, 0, metrics.TasksFailed, "no task should fail")
				require.GreaterOrEqual(t, metrics.MaxActiveExec, int64(2), "single worker should execute tasks concurrently")
			})
		})
	}
}

func TestWorkerStressE2E_MultiWorkerLabelsWeightsBenchmark(t *testing.T) {
	for _, impl := range []workerImpl{implLegacy, implV2} {
		impl := impl
		t.Run(string(impl), func(t *testing.T) {
			withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
				cfg := stressRunConfig{
					Impl:              impl,
					WorkerCount:       3,
					WorkerConcurrency: 4,
					Tasks:             300,
					SleepMs:           30,
					TaskTimeout:       90 * time.Second,
					PollInterval:      5 * time.Millisecond,
					HeartbeatInterval: 30 * time.Millisecond,
					LockTTL:           4 * time.Second,
					LockRefresh:       300 * time.Millisecond,
					LabelsByWorker: [][]string{
						{"w1"},
						{"w2"},
						{"w1", "w2"},
					},
				}

				metrics := runStressE2E(t, ctx, m, cfg)
				logStressMetrics(t, metrics)

				require.Equal(t, cfg.Tasks, metrics.TasksCompleted)
				require.Equal(t, 0, metrics.TasksFailed)
				require.Greater(t, metrics.GroupCompleted["w1"], 0)
				require.Greater(t, metrics.GroupCompleted["w2"], 0)
				require.Greater(t, metrics.GroupCompleted["default"], 0)
			})
		})
	}
}

func runStressE2E(t *testing.T, ctx context.Context, m model.ModelInterface, cfg stressRunConfig) stressMetrics {
	t.Helper()

	require.NoError(t, seedRuntimeConfigForStress(ctx, m))

	exec := newStressProbeExecutor()
	handler := taskgen.NewTaskHandler(exec)
	runner := taskgen.NewTaskRunner(taskcore.NewTaskStore(m))

	stopWorkers := startWorkers(t, m, handler, cfg)
	defer stopWorkers()

	tasks := enqueueStressTasks(t, ctx, runner, cfg)
	terminal := waitStressTasksTerminal(t, ctx, m, tasks, cfg.TaskTimeout)

	latencies := make([]time.Duration, 0, len(terminal))
	groupDone := map[string]int{}
	var totalLatency time.Duration
	for _, done := range terminal {
		lat := done.CompletedAt.Sub(done.Meta.EnqueuedAt)
		if lat < 0 {
			lat = 0
		}
		latencies = append(latencies, lat)
		totalLatency += lat
		if done.Status == "completed" {
			groupDone[done.Meta.Group]++
		}
	}

	tasksCompleted := 0
	tasksFailed := 0
	for _, done := range terminal {
		switch done.Status {
		case "completed":
			tasksCompleted++
		case "failed":
			tasksFailed++
		}
	}

	duration := exec.wallDuration()
	if duration <= 0 {
		duration = time.Millisecond
	}
	mean := time.Duration(0)
	if len(latencies) > 0 {
		mean = time.Duration(int64(totalLatency) / int64(len(latencies)))
	}

	return stressMetrics{
		Impl:              cfg.Impl,
		Workers:           cfg.WorkerCount,
		WorkerConcurrency: cfg.WorkerConcurrency,
		TasksTotal:        cfg.Tasks,
		TasksCompleted:    tasksCompleted,
		TasksFailed:       tasksFailed,
		Duration:          duration,
		ThroughputTPS:     float64(tasksCompleted) / duration.Seconds(),
		MaxActiveExec:     exec.maxActive.Load(),
		P50Latency:        percentileDuration(latencies, 0.50),
		P95Latency:        percentileDuration(latencies, 0.95),
		P99Latency:        percentileDuration(latencies, 0.99),
		MaxLatency:        percentileDuration(latencies, 1.0),
		MeanLatency:       mean,
		GroupCompleted:    groupDone,
	}
}

func logStressMetrics(t *testing.T, m stressMetrics) {
	t.Helper()
	t.Logf("[stress metrics] impl=%s workers=%d worker_concurrency=%d tasks=%d completed=%d failed=%d duration=%s throughput=%.2f/s max_active=%d p50=%s p95=%s p99=%s max=%s mean=%s groups=%v",
		m.Impl,
		m.Workers,
		m.WorkerConcurrency,
		m.TasksTotal,
		m.TasksCompleted,
		m.TasksFailed,
		m.Duration,
		m.ThroughputTPS,
		m.MaxActiveExec,
		m.P50Latency,
		m.P95Latency,
		m.P99Latency,
		m.MaxLatency,
		m.MeanLatency,
		m.GroupCompleted,
	)
}

type stressTaskDone struct {
	TaskID      int32
	Status      string
	CompletedAt time.Time
	Meta        taskMeta
}

func waitStressTasksTerminal(t *testing.T, ctx context.Context, m model.ModelInterface, tasks map[int32]taskMeta, timeout time.Duration) map[int32]stressTaskDone {
	t.Helper()

	deadline := time.Now().Add(timeout)
	out := map[int32]stressTaskDone{}
	for {
		for id, meta := range tasks {
			if _, ok := out[id]; ok {
				continue
			}
			qt, err := m.GetTaskByID(ctx, id)
			if err != nil {
				continue
			}
			if qt.Status == "completed" || qt.Status == "failed" {
				out[id] = stressTaskDone{
					TaskID:      id,
					Status:      qt.Status,
					CompletedAt: qt.UpdatedAt,
					Meta:        meta,
				}
			}
		}
		if len(out) == len(tasks) {
			return out
		}
		if time.Now().After(deadline) {
			missing := len(tasks) - len(out)
			require.FailNowf(t, "stress tasks timeout", "timed out waiting terminal state, missing=%d total=%d", missing, len(tasks))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func enqueueStressTasks(t *testing.T, ctx context.Context, runner taskgen.TaskRunner, cfg stressRunConfig) map[int32]taskMeta {
	t.Helper()

	weightByGroup := map[string]int32{"w1": 5, "w2": 2, "default": 1}
	out := make(map[int32]taskMeta, cfg.Tasks)
	for i := 0; i < cfg.Tasks; i++ {
		group := chooseStressGroup(i)
		params := &taskgen.StressProbeParameters{
			JobID:   int64(i + 1),
			SleepMs: cfg.SleepMs,
			Group:   group,
		}

		overrides := []taskcore.TaskOverride{taskcore.WithWeight(weightByGroup[group])}
		if group != "default" {
			overrides = append(overrides, taskcore.WithLabels([]string{group}))
		}
		id, err := runner.RunStressProbe(ctx, params, overrides...)
		require.NoErrorf(t, err, "enqueue task i=%d group=%s", i, group)
		out[id] = taskMeta{EnqueuedAt: time.Now(), Group: group}
	}
	return out
}

func chooseStressGroup(i int) string {
	mod := i % 10
	switch {
	case mod < 5:
		return "w1"
	case mod < 8:
		return "w2"
	default:
		return "default"
	}
}

func startWorkers(t *testing.T, m model.ModelInterface, handler legacyworker.TaskHandler, cfg stressRunConfig) func() {
	t.Helper()
	require.Greater(t, cfg.WorkerCount, 0)
	require.Greater(t, cfg.WorkerConcurrency, 0)

	dsn := smokePostgresDSN()
	poll := cfg.PollInterval
	heartbeat := cfg.HeartbeatInterval
	lockTTL := cfg.LockTTL
	lockRefresh := cfg.LockRefresh
	concurrency := cfg.WorkerConcurrency
	maxStrict := 100

	globalContexts := make([]*globalctx.GlobalContext, 0, cfg.WorkerCount)
	for i := 0; i < cfg.WorkerCount; i++ {
		labels := []string{}
		if i < len(cfg.LabelsByWorker) {
			labels = append([]string(nil), cfg.LabelsByWorker[i]...)
		}
		workerID := uuid.NewString()
		workerCfg := &config.Config{
			Pg: config.Pg{DSN: &dsn},
			Worker: config.Worker{
				PollInterval:        &poll,
				HeartbeatInterval:   &heartbeat,
				LockTTL:             &lockTTL,
				LockRefreshInterval: &lockRefresh,
				Concurrency:         &concurrency,
				Labels:              labels,
				WorkerID:            &workerID,
				MaxStrictPercentage: &maxStrict,
			},
		}

		gctx := globalctx.New()
		globalContexts = append(globalContexts, gctx)

		switch cfg.Impl {
		case implLegacy:
			w, err := legacyworker.NewWorker(gctx, workerCfg, m, handler)
			require.NoError(t, err)
			go w.Start()
		case implV2:
			w, err := workerv2.NewWorker(gctx, workerCfg, m, handler)
			require.NoError(t, err)
			go w.Start()
		default:
			require.FailNowf(t, "unknown worker impl", "impl=%s", cfg.Impl)
		}
	}

	time.Sleep(150 * time.Millisecond)

	return func() {
		for _, g := range globalContexts {
			g.Cancel()
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func seedRuntimeConfigForStress(ctx context.Context, m model.ModelInterface) error {
	maxStrict := int32(100)
	payload := map[string]any{
		"maxStrictPercentage": maxStrict,
		"labelWeights": map[string]int32{
			"default": 1,
			"w1":      5,
			"w2":      2,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	cfg, err := m.CreateWorkerRuntimeConfig(ctx, raw)
	if err != nil {
		return err
	}
	notifyPayload := map[string]any{
		"op": "up_config",
		"params": map[string]any{
			"request_id": "stress-seed",
			"version":    cfg.Version,
		},
	}
	notifyRaw, err := json.Marshal(notifyPayload)
	if err != nil {
		return err
	}
	return m.NotifyWorkerRuntimeConfig(ctx, string(notifyRaw))
}

func percentileDuration(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	buf := append([]time.Duration(nil), values...)
	sort.Slice(buf, func(i, j int) bool { return buf[i] < buf[j] })
	idx := int(math.Ceil(float64(len(buf))*p)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(buf) {
		idx = len(buf) - 1
	}
	return buf[idx]
}

type stressProbeExecutor struct {
	active    atomic.Int64
	maxActive atomic.Int64
	execTotal atomic.Int64
	startedAt atomic.Int64
	endedAt   atomic.Int64
}

func newStressProbeExecutor() *stressProbeExecutor {
	return &stressProbeExecutor{}
}

func (e *stressProbeExecutor) markStart() {
	now := time.Now().UnixNano()
	for {
		cur := e.startedAt.Load()
		if cur != 0 && cur <= now {
			break
		}
		if e.startedAt.CompareAndSwap(cur, now) {
			break
		}
	}
}

func (e *stressProbeExecutor) markEnd() {
	e.endedAt.Store(time.Now().UnixNano())
}

func (e *stressProbeExecutor) wallDuration() time.Duration {
	start := e.startedAt.Load()
	end := e.endedAt.Load()
	if start == 0 || end == 0 || end < start {
		return 0
	}
	return time.Duration(end - start)
}

func (e *stressProbeExecutor) ExecuteDeleteOpaqueKey(ctx context.Context, params *taskgen.DeleteOpaqueKeyParameters) error {
	return nil
}

func (e *stressProbeExecutor) OnDeleteOpaqueKeyFailed(ctx context.Context, taskID int32, params *taskgen.DeleteOpaqueKeyParameters, tx core.Tx) error {
	return nil
}

func (e *stressProbeExecutor) ExecuteUpdateWorkerRuntimeConfig(ctx context.Context, params *taskgen.UpdateWorkerRuntimeConfigParameters) error {
	return nil
}

func (e *stressProbeExecutor) ExecuteStressProbe(ctx context.Context, params *taskgen.StressProbeParameters) error {
	e.markStart()
	active := e.active.Add(1)
	for {
		max := e.maxActive.Load()
		if active <= max {
			break
		}
		if e.maxActive.CompareAndSwap(max, active) {
			break
		}
	}
	defer func() {
		e.active.Add(-1)
		e.execTotal.Add(1)
		e.markEnd()
	}()

	d := time.Duration(params.SleepMs) * time.Millisecond
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
