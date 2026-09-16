//go:build smoke

package taskcoree2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type arrivalPhase struct {
	Name          string
	Seconds, Rate int
}

type arrivalInput struct {
	DueAt   time.Time     `json:"due_at"`
	Phase   string        `json:"phase"`
	Kind    string        `json:"kind"`
	DelayMS int           `json:"delay_ms"`
	Tags    []string      `json:"tags"`
	Serial  string        `json:"serial"`
	Offset  time.Duration `json:"-"`
}

type arrivalLatency struct {
	Count                      int
	P50MS, P95MS, P99MS, MaxMS float64
}
type arrivalCohort struct {
	EnqueueLag, DueToReady, ReadyToClaim, ClaimToHandler, EndToEnd arrivalLatency
}
type arrivalSample struct {
	Seconds                              float64
	Submitted, Ready, Running, Completed int
}
type arrivalWindow struct {
	Name                                                              string
	Seconds, CPUSeconds, SQLMS                                        float64
	Submitted, Completed, AdmissionCalls, EmptyAdmissions, ProbeCalls int
}
type arrivalResult struct {
	Revision, Scenario, Postgres                                                            string
	Workers, SlotsPerWorker, Offered, Completed, Attempts, RemainingPermits                 int
	ElapsedSeconds, CPUSeconds, SQLMS, ProducerMaxLagMS, ReleaseRecoveryMS, QuotaRecoveryMS float64
	LeaseErrors, LeaseLost                                                                  float64
	Windows                                                                                 []arrivalWindow
	Samples                                                                                 []arrivalSample
	Cohorts                                                                                 map[string]arrivalCohort
	Operations                                                                              map[string]*admissionBenchOperation
	Passed                                                                                  bool
}

type arrivalHandler struct {
	admissionBenchHandler
	mu     sync.Mutex
	starts map[int32]time.Time
}

func (h *arrivalHandler) HandleTask(ctx context.Context, task worker.Task) error {
	h.mu.Lock()
	h.starts[task.ID] = time.Now()
	h.mu.Unlock()
	var payload struct {
		DelayMS int `json:"delay_ms"`
	}
	if err := json.Unmarshal(task.GetPayload(), &payload); err != nil {
		return err
	}
	timer := time.NewTimer(time.Duration(payload.DelayMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Arrivals follow wall-clock schedules, independently of task completion. The
// producer records missed delivery times rather than reducing offered load.
// Copy this file AND admission_benchmark_test.go unchanged to both revisions.
func TestTaskPrefetchArrivalBenchmark(t *testing.T) {
	if os.Getenv("ANCLAX_ARRIVAL_BENCH") != "1" {
		t.Skip("opt-in persistent-runtime arrival benchmark")
	}
	ctx := context.Background()
	name := fmt.Sprintf("anclax-arrival-bench-%d", os.Getpid())
	image := os.Getenv("ANCLAX_SMOKE_POSTGRES_IMAGE")
	if image == "" {
		image = "postgres:17"
	}
	require.NoError(t, runDocker(t, "run", "-d", "--name", name, "--cpus=2", "--memory=1g", "-e", "POSTGRES_PASSWORD=postgres", "-p", "127.0.0.1::5432", image,
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
	scenarios := os.Getenv("ANCLAX_ARRIVAL_BENCH_SCENARIOS")
	if scenarios == "" {
		scenarios = "flow,mixed,recovery"
	}
	var results []arrivalResult
	t.Cleanup(func() { writeTagTestReport(t, "prefetch-arrival-benchmark", results) })
	for _, scenario := range strings.Split(scenarios, ",") {
		t.Run(scenario, func(t *testing.T) { results = append(results, runArrivalCase(t, ctx, conn, dsn, name, scenario)) })
	}
}

func runArrivalCase(t *testing.T, ctx context.Context, conn *pgx.Conn, dsn, name, scenario string) arrivalResult {
	t.Helper()
	require.Contains(t, []string{"flow", "mixed", "recovery"}, scenario)
	workerCount, slots := 1, 100
	if scenario == "mixed" {
		workerCount, slots = 2, 50
	}
	if scenario == "recovery" {
		slots = 8
	}
	poll, heartbeat, ttl, strict, renewal := 20*time.Millisecond, time.Second, 9*time.Second, 0, int32(10)
	cfg := &config.Config{Pg: config.Pg{DSN: &dsn}, Worker: config.Worker{Concurrency: &slots, PollInterval: &poll, HeartbeatInterval: &heartbeat,
		LockRefreshInterval: &heartbeat, LockTTL: &ttl, MaxStrictPercentage: &strict, LeaseRenewalMaxConnections: &renewal}}
	lib := config.DefaultLibConfig()
	lib.Pg.MaxConnections, lib.Pg.MinConnections = 50, 1
	cm := closer.NewCloserManager()
	defer cm.Close()
	base, err := model.NewModel(cfg, lib, cm)
	require.NoError(t, err)
	require.NoError(t, resetDSTState(ctx, base))
	// Benchmark-only tracing adds equal database work to both versions. No
	// production trigger/schema is changed. All cohorts are evaluated at drain,
	// so tasks waiting beyond a phase are not omitted from latency percentiles.
	_, err = conn.Exec(ctx, `DROP TABLE IF EXISTS public.arrival_trace CASCADE;
CREATE UNLOGGED TABLE public.arrival_trace(task_id INT PRIMARY KEY,phase TEXT,kind TEXT,due_at TIMESTAMPTZ,inserted_at TIMESTAMPTZ,
 ready_at TIMESTAMPTZ,claim_at TIMESTAMPTZ,finished_at TIMESTAMPTZ);
CREATE OR REPLACE FUNCTION public.arrival_audit() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.spec->>'type'='arrival-benchmark' AND NEW.status IS DISTINCT FROM OLD.status THEN
  UPDATE public.arrival_trace SET ready_at=CASE WHEN NEW.status='ready' THEN COALESCE(ready_at,clock_timestamp()) ELSE ready_at END,
   claim_at=CASE WHEN NEW.status='running' THEN COALESCE(claim_at,clock_timestamp()) ELSE claim_at END,
   finished_at=CASE WHEN NEW.status='completed' THEN clock_timestamp() ELSE finished_at END WHERE task_id=NEW.id;
 END IF; RETURN NEW; END; $$;
DROP TRIGGER IF EXISTS arrival_audit ON anclax.tasks;
CREATE TRIGGER arrival_audit AFTER UPDATE OF status ON anclax.tasks FOR EACH ROW EXECUTE FUNCTION public.arrival_audit();`)
	require.NoError(t, err)
	for tag, limit := range map[string]int32{"arrival:hot": 8, "arrival:release": 8, "arrival:disabled": 0} {
		require.NoError(t, base.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: tag, MaxConcurrency: limit}))
	}
	stats := &admissionBenchStats{operations: map[string]*admissionBenchOperation{}}
	observed := &admissionBenchModel{ModelInterface: base, stats: stats}
	handler := &arrivalHandler{starts: map[int32]time.Time{}}
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	for i := 0; i < workerCount; i++ {
		components, err := worker.BuildWorkerComponents(cfg, observed, handler)
		require.NoError(t, err)
		wg.Add(1)
		go func() { defer wg.Done(); components.Runtime.Start(runCtx) }()
	}
	time.Sleep(2 * time.Second)
	stats.mu.Lock()
	stats.operations = map[string]*admissionBenchOperation{}
	stats.mu.Unlock()
	_, err = conn.Exec(ctx, "SELECT pg_stat_statements_reset()")
	require.NoError(t, err)
	leaseErrors, leaseLost := metricValue(metrics.TaskLeaseRenewalErrorsTotal), metricValue(metrics.TaskLeaseRenewalLostTotal)
	producer, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer producer.Close(ctx)
	phases, inputs := arrivalWorkload(scenario)
	started := time.Now().Add(200 * time.Millisecond)
	for i := range inputs {
		inputs[i].DueAt = started.Add(inputs[i].Offset)
	}
	result := arrivalResult{Revision: os.Getenv("ANCLAX_ADMISSION_BENCH_REVISION"), Scenario: scenario, Workers: workerCount, SlotsPerWorker: slots, Offered: len(inputs)}
	require.NoError(t, conn.QueryRow(ctx, "SHOW server_version").Scan(&result.Postgres))
	cpuStart := admissionBenchCPU(t, name)
	type producerResult struct {
		lag float64
		err error
	}
	produced := make(chan producerResult, 1)
	go func() {
		lag, err := produceArrivals(runCtx, producer, started, inputs)
		produced <- producerResult{lag, err}
	}()
	// Ensure producer goroutines finish before closing connections on any failure.
	var producerDone bool
	defer func() {
		cancel()
		if !producerDone {
			<-produced
		}
	}()
	last := arrivalWindow{}
	boundary := started
	sampleTick := time.NewTicker(250 * time.Millisecond)
	defer sampleTick.Stop()
	quotaEnabled := false
	var quotaAt time.Time
	for _, phase := range append(phases, arrivalPhase{Name: "drain", Seconds: 30}) {
		if phase.Name == "quota-enabled" {
			require.NoError(t, base.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "arrival:disabled", MaxConcurrency: 8}))
			quotaAt = time.Now()
			quotaEnabled = true
		}
		boundary = boundary.Add(time.Duration(phase.Seconds) * time.Second)
		for {
			select {
			case <-sampleTick.C:
			case p := <-produced:
				producerDone = true
				result.ProducerMaxLagMS = p.lag
				require.NoError(t, p.err)
			}
			sample := arrivalSample{Seconds: time.Since(started).Seconds()}
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE ready_at IS NOT NULL AND claim_at IS NULL),
 count(*) FILTER(WHERE claim_at IS NOT NULL AND finished_at IS NULL),count(*) FILTER(WHERE finished_at IS NOT NULL) FROM public.arrival_trace`).Scan(&sample.Submitted, &sample.Ready, &sample.Running, &sample.Completed))
			result.Samples = append(result.Samples, sample)
			if !time.Now().Before(boundary) || (phase.Name == "drain" && producerDone && sample.Completed == len(inputs)) {
				break
			}
		}
		current := arrivalWindow{Seconds: time.Since(started).Seconds(), CPUSeconds: admissionBenchCPU(t, name) - cpuStart}
		sample := result.Samples[len(result.Samples)-1]
		current.Submitted, current.Completed = sample.Submitted, sample.Completed
		require.NoError(t, conn.QueryRow(ctx, "SELECT COALESCE(sum(total_exec_time),0) FROM pg_stat_statements WHERE toplevel").Scan(&current.SQLMS))
		stats.mu.Lock()
		if op := stats.operations["PrefetchReadyTasks"]; op != nil {
			current.AdmissionCalls, current.EmptyAdmissions = op.Calls, op.ZeroPrepared
		}
		if op := stats.operations["InspectTaskPrefetch"]; op != nil {
			current.ProbeCalls = op.Calls
		}
		stats.mu.Unlock()
		result.Windows = append(result.Windows, arrivalWindow{Name: phase.Name, Seconds: current.Seconds - last.Seconds, CPUSeconds: current.CPUSeconds - last.CPUSeconds,
			SQLMS: current.SQLMS - last.SQLMS, Submitted: current.Submitted - last.Submitted, Completed: current.Completed - last.Completed,
			AdmissionCalls: current.AdmissionCalls - last.AdmissionCalls, EmptyAdmissions: current.EmptyAdmissions - last.EmptyAdmissions, ProbeCalls: current.ProbeCalls - last.ProbeCalls})
		last = current
		t.Logf("ARRIVAL scenario=%s phase=%s committed=%d completed=%d cpu=%.3f", scenario, phase.Name, current.Submitted, current.Completed, current.CPUSeconds)
	}
	cancel()
	wg.Wait()
	if !producerDone {
		p := <-produced
		producerDone = true
		result.ProducerMaxLagMS = p.lag
		require.NoError(t, p.err)
	}
	result.ElapsedSeconds, result.CPUSeconds, result.SQLMS = last.Seconds, last.CPUSeconds, last.SQLMS
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='completed'),COALESCE(sum(attempts),0)
 FROM anclax.tasks WHERE spec->>'type'='arrival-benchmark'`).Scan(&result.Completed, &result.Attempts))
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM anclax.task_tag_permits").Scan(&result.RemainingPermits))
	result.LeaseErrors, result.LeaseLost = metricValue(metrics.TaskLeaseRenewalErrorsTotal)-leaseErrors, metricValue(metrics.TaskLeaseRenewalLostTotal)-leaseLost
	stats.mu.Lock()
	result.Operations = stats.operations
	for _, op := range result.Operations {
		op.summarize()
	}
	stats.mu.Unlock()
	result.Cohorts = arrivalCohorts(t, ctx, conn, handler)
	if scenario == "recovery" {
		require.True(t, quotaEnabled)
		require.NoError(t, conn.QueryRow(ctx, `SELECT extract(epoch FROM ((SELECT min(ready_at) FROM public.arrival_trace WHERE kind='release-waiter')-
 (SELECT min(finished_at) FROM public.arrival_trace WHERE kind='holder')))*1000`).Scan(&result.ReleaseRecoveryMS))
		require.NoError(t, conn.QueryRow(ctx, "SELECT extract(epoch FROM (min(ready_at)-$1::timestamptz))*1000 FROM public.arrival_trace WHERE kind='disabled'", quotaAt).Scan(&result.QuotaRecoveryMS))
		var holdersFirst bool
		require.NoError(t, conn.QueryRow(ctx, `SELECT (SELECT max(claim_at) FROM public.arrival_trace WHERE kind='holder')<
 (SELECT min(ready_at) FROM public.arrival_trace WHERE kind='stocked')`).Scan(&holdersFirst))
		require.True(t, holdersFirst, "fixture must occupy every execution slot before stocking unrelated ready tasks")
		require.GreaterOrEqual(t, result.ReleaseRecoveryMS, float64(0))
		require.GreaterOrEqual(t, result.QuotaRecoveryMS, float64(-10), "client timestamp is taken immediately after quota commit")
	}
	result.Passed = result.Completed == result.Offered && result.Attempts == result.Offered && result.RemainingPermits == 0 && result.LeaseErrors == 0 && result.LeaseLost == 0
	for _, op := range result.Operations {
		if len(op.Errors) > 0 {
			result.Passed = false
		}
	}
	if !result.Passed {
		t.Error("arrival workload failed completion, attempt, permit, lease or operation-error checks")
	}
	return result
}

func arrivalWorkload(scenario string) ([]arrivalPhase, []arrivalInput) {
	var phases []arrivalPhase
	switch scenario {
	case "flow":
		phases = []arrivalPhase{{"idle", 2, 0}, {"low", 4, 50}, {"high", 6, 1500}, {"fall", 4, 100}, {"idle-again", 3, 0}, {"burst", 3, 2500}, {"recover", 4, 200}}
	case "mixed":
		phases = []arrivalPhase{{"idle", 2, 0}, {"mixed", 6, 80}, {"pressure", 8, 200}, {"fall", 4, 40}, {"idle-again", 3, 0}}
	case "recovery":
		phases = []arrivalPhase{{"held", 4, 0}, {"quota-enabled", 3, 0}, {"idle", 2, 0}, {"new-arrival", 3, 0}}
	}
	var tasks []arrivalInput
	var offset time.Duration
	for _, p := range phases {
		for n := 0; n < p.Seconds*p.Rate; n++ {
			task := arrivalInput{Offset: offset + time.Duration(float64(n)/float64(p.Rate)*float64(time.Second)), Phase: p.Name, Kind: "short", DelayMS: 20, Tags: []string{}}
			if scenario == "mixed" {
				i := len(tasks)
				if i%2 == 0 {
					task.Tags = []string{"arrival:hot"}
					task.Kind = "hot"
				}
				if i%10 == 1 {
					task.Serial = fmt.Sprintf("arrival:serial:%d", (i/10)%4)
					task.Kind = "serial"
				}
				if i%20 == 0 {
					task.DelayMS = 2000
				} else if i%5 == 0 {
					task.DelayMS = 200
				}
				if task.Serial != "" && (i/10)%5 == 0 {
					task.DelayMS = 1000
				}
			}
			tasks = append(tasks, task)
		}
		offset += time.Duration(p.Seconds) * time.Second
	}
	if scenario == "recovery" {
		add := func(n int, at time.Duration, phase, kind, tag string, delay int) {
			for i := 0; i < n; i++ {
				tags := []string{}
				if tag != "" {
					tags = []string{tag}
				}
				tasks = append(tasks, arrivalInput{Offset: at, Phase: phase, Kind: kind, Tags: tags, DelayMS: delay})
			}
		}
		add(8, 0, "held", "holder", "arrival:release", 3000)
		add(16, 500*time.Millisecond, "held", "release-waiter", "arrival:release", 20)
		add(16, 500*time.Millisecond, "held", "stocked", "", 20)
		add(16, 500*time.Millisecond, "held", "disabled", "arrival:disabled", 20)
		add(32, 9*time.Second, "new-arrival", "new-arrival", "", 20)
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].Offset < tasks[j].Offset })
	return phases, tasks
}

func produceArrivals(ctx context.Context, conn *pgx.Conn, start time.Time, tasks []arrivalInput) (maxLagMS float64, err error) {
	for i := 0; i < len(tasks); {
		timer := time.NewTimer(max(0, time.Until(start.Add(tasks[i].Offset))))
		select {
		case <-ctx.Done():
			timer.Stop()
			return maxLagMS, ctx.Err()
		case <-timer.C:
		}
		// Coalesce arrivals into at most 20 ms, independently of consumption.
		timer.Reset(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return maxLagMS, ctx.Err()
		case <-timer.C:
		}
		now := time.Now()
		j := i
		for j < len(tasks) && j-i < 256 && !start.Add(tasks[j].Offset).After(now) {
			j++
		}
		data, err := json.Marshal(tasks[i:j])
		if err != nil {
			return maxLagMS, err
		}
		_, err = conn.Exec(ctx, `WITH added AS (INSERT INTO anclax.tasks(attributes,spec,status,started_at,serial_key)
 SELECT jsonb_build_object('tags',x.tags),jsonb_build_object('type','arrival-benchmark','payload',jsonb_build_object('phase',x.phase,'kind',x.kind,'delay_ms',x.delay_ms)),
 'pending',x.due_at,NULLIF(x.serial,'') FROM jsonb_to_recordset($1::jsonb) AS x(due_at timestamptz,phase text,kind text,delay_ms int,tags jsonb,serial text)
 RETURNING id,spec,started_at,created_at)
 INSERT INTO public.arrival_trace(task_id,phase,kind,due_at,inserted_at)
 SELECT id,spec->'payload'->>'phase',spec->'payload'->>'kind',started_at,clock_timestamp() FROM added`, data)
		if err != nil {
			return maxLagMS, err
		}
		maxLagMS = max(maxLagMS, float64(time.Since(tasks[i].DueAt))/float64(time.Millisecond))
		i = j
	}
	return maxLagMS, nil
}

func arrivalCohorts(t *testing.T, ctx context.Context, conn *pgx.Conn, h *arrivalHandler) map[string]arrivalCohort {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT task_id,phase,kind,due_at,inserted_at,ready_at,claim_at,finished_at FROM public.arrival_trace ORDER BY task_id`)
	require.NoError(t, err)
	defer rows.Close()
	values := map[string][][]float64{}
	for rows.Next() {
		var id int32
		var phase, kind string
		var due, inserted, ready, claim, finish time.Time
		require.NoError(t, rows.Scan(&id, &phase, &kind, &due, &inserted, &ready, &claim, &finish))
		started, ok := h.starts[id]
		require.True(t, ok)
		for _, key := range []string{"all", "phase:" + phase, "kind:" + kind} {
			if values[key] == nil {
				values[key] = make([][]float64, 5)
			}
			for i, d := range []time.Duration{inserted.Sub(due), ready.Sub(due), claim.Sub(ready), started.Sub(claim), finish.Sub(due)} {
				values[key][i] = append(values[key][i], float64(d)/float64(time.Millisecond))
			}
		}
	}
	require.NoError(t, rows.Err())
	result := map[string]arrivalCohort{}
	for key, v := range values {
		result[key] = arrivalCohort{arrivalPercentiles(v[0]), arrivalPercentiles(v[1]), arrivalPercentiles(v[2]), arrivalPercentiles(v[3]), arrivalPercentiles(v[4])}
	}
	return result
}
func arrivalPercentiles(values []float64) arrivalLatency {
	sort.Float64s(values)
	n := len(values)
	if n == 0 {
		return arrivalLatency{}
	}
	return arrivalLatency{n, values[(n*50+99)/100-1], values[(n*95+99)/100-1], values[(n*99+99)/100-1], values[n-1]}
}
