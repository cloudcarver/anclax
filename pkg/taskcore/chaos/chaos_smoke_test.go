//go:build smoke
// +build smoke

package chaos

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type chaosWorkerSlot struct {
	name      string
	labels    []string
	active    bool
	retired   bool
	restartAt int
}

type chaosState struct {
	rng                   *rand.Rand
	workers               []*chaosWorkerSlot
	nextReplacement       int
	controlPlaneDownUntil int
	expected              map[string]struct{}
	tasksSubmitted        int
	workerDisruptions     int
	postgresRestarts      int
	controlPlaneOutages   int
	runtimeConfigUpdates  int
	replacementWorkers    int
	componentDowns        map[string]int
	componentRestarts     map[string]int
}

func TestContainerizedTaskcoreChaosSmoke(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	cfg := DefaultRunConfig()
	cfg.Seed = readChaosInt64Env(t, "ANCLAX_TASKCORE_CHAOS_SEED", 424242)
	cfg.KeepArtifacts = true

	h, err := NewHarness(cfg)
	require.NoError(t, err)
	ctx := context.Background()
	var runErr error
	var state *chaosState
	defer func() {
		if summary, err := buildSmokeSummary(ctx, h, state); err == nil {
			h.Report().SetSummary(summary)
			if summary != nil {
				t.Logf("chaos summary: tasks submitted=%d completed=%d retried=%d downs=%v restarts=%v", summary.Tasks.Submitted, summary.Tasks.Completed, summary.Tasks.Retried, summary.Components.DownCounts, summary.Components.RestartCounts)
			}
		} else {
			h.Report().AddEvent("summary.error", "report", err.Error(), nil)
		}
		if runErr != nil {
			_ = h.CollectDiagnostics(ctx, runErr)
		}
		_ = h.Close(ctx)
		t.Logf("chaos artifacts: %s", h.ArtifactDir())
	}()

	must := func(err error) {
		if err != nil {
			runErr = err
			t.Fatal(err)
		}
	}

	must(h.Start(ctx))
	state = &chaosState{
		rng: rand.New(rand.NewSource(cfg.Seed)),
		workers: []*chaosWorkerSlot{
			{name: "worker-a", labels: []string{"w1"}},
			{name: "worker-b", labels: []string{"w2"}},
			{name: "worker-c", labels: []string{"w1", "w2"}},
		},
		expected:          map[string]struct{}{},
		componentDowns:    map[string]int{},
		componentRestarts: map[string]int{},
	}
	for _, slot := range state.workers {
		must(h.StartWorker(ctx, slot.name, slot.labels))
		slot.active = true
		must(h.WaitWorkerOnline(ctx, slot.name, true, 20*time.Second))
	}

	user := h.User()
	iterations := readChaosPositiveEnvInt(t, "ANCLAX_TASKCORE_CHAOS_ITERATIONS", 28)
	interIterSleep := time.Duration(readChaosPositiveEnvInt(t, "ANCLAX_TASKCORE_CHAOS_INTER_ITER_SLEEP_MS", 300)) * time.Millisecond
	for iter := 1; iter <= iterations; iter++ {
		must(reconcileChaosState(ctx, h, state, iter))
		if state.controlPlaneAvailable(iter) {
			must(submitChaosBatch(ctx, user, state, iter))
		}
		must(runRandomAction(ctx, h, state, iter))
		time.Sleep(interIterSleep)
	}

	must(restoreCluster(ctx, h, state, iterations+1))
	for uniqueTag := range state.expected {
		must(user.ExpectCompleted(ctx, uniqueTag, 60*time.Second))
	}

	pending, err := h.Inspector().CountTasksByStatuses(ctx, []string{"pending", "running"}, "LONG-")
	must(err)
	require.Equal(t, int64(0), pending)
	if state.workerDisruptions > 0 || state.postgresRestarts > 0 {
		retried, err := h.Inspector().CountRetriedTasks(ctx, "LONG-")
		must(err)
		require.Greater(t, retried, int64(0))
	}
	require.Greater(t, len(state.expected), 0)
}

func buildSmokeSummary(ctx context.Context, h *Harness, state *chaosState) (*ReportSummary, error) {
	if h == nil || h.Inspector() == nil || state == nil {
		return nil, nil
	}
	inspector := h.Inspector()
	observed, err := inspector.CountTasks(ctx, "LONG-")
	if err != nil {
		return nil, err
	}
	completed, err := inspector.CountTasksByStatuses(ctx, []string{"completed"}, "LONG-")
	if err != nil {
		return nil, err
	}
	pending, err := inspector.CountTasksByStatuses(ctx, []string{"pending"}, "LONG-")
	if err != nil {
		return nil, err
	}
	running, err := inspector.CountTasksByStatuses(ctx, []string{"running"}, "LONG-")
	if err != nil {
		return nil, err
	}
	failed, err := inspector.CountTasksByStatuses(ctx, []string{"failed"}, "LONG-")
	if err != nil {
		return nil, err
	}
	cancelled, err := inspector.CountTasksByStatuses(ctx, []string{"cancelled"}, "LONG-")
	if err != nil {
		return nil, err
	}
	paused, err := inspector.CountTasksByStatuses(ctx, []string{"paused"}, "LONG-")
	if err != nil {
		return nil, err
	}
	retried, err := inspector.CountRetriedTasks(ctx, "LONG-")
	if err != nil {
		return nil, err
	}
	retiredWorkers := 0
	for _, slot := range state.workers {
		if slot != nil && slot.retired {
			retiredWorkers++
		}
	}
	processed := completed + failed + cancelled + paused
	return &ReportSummary{
		Components: ComponentSummary{
			DownCounts:    cloneIntMap(state.componentDowns),
			RestartCounts: cloneIntMap(state.componentRestarts),
		},
		Tasks: TaskSummary{
			Submitted: state.tasksSubmitted,
			Expected:  len(state.expected),
			Observed:  observed,
			Processed: processed,
			Completed: completed,
			Pending:   pending,
			Running:   running,
			Failed:    failed,
			Cancelled: cancelled,
			Paused:    paused,
			Retried:   retried,
		},
		Scenario: ScenarioSummary{
			WorkerDisruptions:    state.workerDisruptions,
			PostgresRestarts:     state.postgresRestarts,
			ControlPlaneOutages:  state.controlPlaneOutages,
			RuntimeConfigUpdates: state.runtimeConfigUpdates,
			ReplacementWorkers:   state.replacementWorkers,
			ActiveWorkers:        state.activeWorkers(),
			RetiredWorkers:       retiredWorkers,
		},
	}, nil
}

func cloneIntMap(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func submitChaosBatch(ctx context.Context, user *User, state *chaosState, iter int) error {
	batchSize := readChaosPositiveEnvInt(nil, "ANCLAX_TASKCORE_CHAOS_BATCH_SIZE", 3)
	taskSleepMs := int32(readChaosPositiveEnvInt(nil, "ANCLAX_TASKCORE_CHAOS_TASK_SLEEP_MS", 400))
	for j := 0; j < batchSize; j++ {
		taskName := fmt.Sprintf("LONG-%03d-%02d", iter, j)
		labels, group := taskLabelsAndGroup(j)
		if err := user.SubmitStressProbe(ctx, SubmitStressProbeRequest{
			TaskName: taskName,
			JobID:    int64(iter*100 + j),
			SleepMs:  taskSleepMs,
			Group:    group,
			Labels:   labels,
		}); err != nil {
			return err
		}
		state.expected[taskName] = struct{}{}
		state.tasksSubmitted++
	}
	return nil
}

func taskLabelsAndGroup(i int) ([]string, string) {
	switch i % 3 {
	case 1:
		return []string{"w1"}, "w1"
	case 2:
		return []string{"w2"}, "w2"
	default:
		return nil, "default"
	}
}

func reconcileChaosState(ctx context.Context, h *Harness, state *chaosState, iter int) error {
	if state.controlPlaneDownUntil > 0 && iter > state.controlPlaneDownUntil {
		if err := h.StartControlPlane(ctx); err != nil {
			return err
		}
		state.componentRestarts[h.controlPlaneName]++
		state.controlPlaneDownUntil = 0
	}
	for _, slot := range state.workers {
		if slot == nil || slot.retired || slot.active || slot.restartAt == 0 || slot.restartAt > iter {
			continue
		}
		if err := h.StartWorker(ctx, slot.name, slot.labels); err != nil {
			return err
		}
		state.componentRestarts[slot.name]++
		if err := h.WaitWorkerOnline(ctx, slot.name, true, 20*time.Second); err != nil {
			return err
		}
		slot.active = true
		slot.restartAt = 0
	}
	return nil
}

func runRandomAction(ctx context.Context, h *Harness, state *chaosState, iter int) error {
	actions := []func(context.Context, *Harness, *chaosState, int) (bool, error){
		actionWorkerDown,
		actionWorkerRetireAndReplace,
		actionControlPlaneDown,
		actionRuntimeConfig,
		actionPostgresRestart,
		actionWorkerRejoinEarly,
	}
	start := state.rng.Intn(len(actions))
	for i := 0; i < len(actions); i++ {
		applied, err := actions[(start+i)%len(actions)](ctx, h, state, iter)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
	return nil
}

func actionWorkerDown(ctx context.Context, h *Harness, state *chaosState, iter int) (bool, error) {
	slot := state.randomActiveWorker()
	if slot == nil {
		return false, nil
	}
	if err := h.StopWorker(ctx, slot.name, false); err != nil {
		return false, err
	}
	slot.active = false
	slot.restartAt = iter + state.randBetween(2, 5)
	state.workerDisruptions++
	state.componentDowns[slot.name]++
	h.Report().AddEvent("chaos.worker_down", slot.name, "worker stopped", map[string]any{"restartAt": slot.restartAt})
	return true, nil
}

func actionWorkerRetireAndReplace(ctx context.Context, h *Harness, state *chaosState, iter int) (bool, error) {
	slot := state.randomActiveWorker()
	if slot == nil {
		return false, nil
	}
	if err := h.StopWorker(ctx, slot.name, true); err != nil {
		return false, err
	}
	slot.active = false
	slot.retired = true
	state.componentDowns[slot.name]++
	state.nextReplacement++
	repl := &chaosWorkerSlot{
		name:      fmt.Sprintf("worker-repl-%02d", state.nextReplacement),
		labels:    append([]string(nil), slot.labels...),
		restartAt: iter + state.randBetween(2, 4),
	}
	state.workers = append(state.workers, repl)
	state.workerDisruptions++
	state.replacementWorkers++
	h.Report().AddEvent("chaos.worker_replace", slot.name, "worker retired and replacement scheduled", map[string]any{"replacement": repl.name, "restartAt": repl.restartAt})
	return true, nil
}

func actionControlPlaneDown(ctx context.Context, h *Harness, state *chaosState, iter int) (bool, error) {
	if !state.controlPlaneAvailable(iter) {
		return false, nil
	}
	if err := h.StopControlPlane(ctx); err != nil {
		return false, err
	}
	state.controlPlaneDownUntil = iter + state.randBetween(2, 4)
	state.controlPlaneOutages++
	state.componentDowns[h.controlPlaneName]++
	h.Report().AddEvent("chaos.control_plane_down", h.controlPlaneName, "control plane stopped", map[string]any{"restartAt": state.controlPlaneDownUntil})
	return true, nil
}

func actionRuntimeConfig(ctx context.Context, h *Harness, state *chaosState, iter int) (bool, error) {
	if !state.controlPlaneAvailable(iter) || state.activeWorkers() == 0 {
		return false, nil
	}
	req := RuntimeConfigRequest{
		MaxStrictPercentage: int32(state.randBetween(0, 60)),
		DefaultWeight:       1,
		LabelWeights: map[string]int32{
			"w1": int32(state.randBetween(1, 4)),
			"w2": int32(state.randBetween(1, 4)),
		},
	}
	if err := h.User().Control.UpdateRuntimeConfig(ctx, req); err != nil {
		return false, err
	}
	state.runtimeConfigUpdates++
	h.Report().AddEvent("chaos.runtime_config", "control-plane", "runtime config updated", map[string]any{"iter": iter, "weights": req.LabelWeights})
	return true, nil
}

func actionPostgresRestart(ctx context.Context, h *Harness, state *chaosState, iter int) (bool, error) {
	if err := h.RestartPostgres(ctx); err != nil {
		return false, err
	}
	state.componentDowns[h.postgresName]++
	state.componentRestarts[h.postgresName]++
	for _, slot := range state.workers {
		if slot == nil || slot.retired || !slot.active {
			continue
		}
		if err := h.StopWorker(ctx, slot.name, true); err != nil {
			return false, err
		}
		slot.active = false
		state.componentDowns[slot.name]++
		if state.rng.Intn(100) < 20 {
			slot.retired = true
			state.nextReplacement++
			state.replacementWorkers++
			state.workers = append(state.workers, &chaosWorkerSlot{
				name:      fmt.Sprintf("worker-repl-%02d", state.nextReplacement),
				labels:    append([]string(nil), slot.labels...),
				restartAt: iter + state.randBetween(2, 4),
			})
		} else {
			slot.restartAt = iter + state.randBetween(2, 5)
		}
	}
	state.postgresRestarts++
	h.Report().AddEvent("chaos.postgres_restart", h.postgresName, "postgres restarted", map[string]any{"iter": iter})
	return true, nil
}

func actionWorkerRejoinEarly(ctx context.Context, h *Harness, state *chaosState, iter int) (bool, error) {
	slot := state.randomRestartableWorker(iter)
	if slot == nil {
		return false, nil
	}
	if err := h.StartWorker(ctx, slot.name, slot.labels); err != nil {
		return false, err
	}
	state.componentRestarts[slot.name]++
	if err := h.WaitWorkerOnline(ctx, slot.name, true, 20*time.Second); err != nil {
		return false, err
	}
	slot.active = true
	slot.restartAt = 0
	h.Report().AddEvent("chaos.worker_rejoin", slot.name, "worker rejoined early", map[string]any{"iter": iter})
	return true, nil
}

func restoreCluster(ctx context.Context, h *Harness, state *chaosState, iter int) error {
	if !state.controlPlaneAvailable(iter) {
		if err := h.StartControlPlane(ctx); err != nil {
			return err
		}
		state.componentRestarts[h.controlPlaneName]++
		state.controlPlaneDownUntil = 0
	}
	for _, slot := range state.workers {
		if slot == nil || slot.retired || slot.active {
			continue
		}
		slot.restartAt = iter
	}
	return reconcileChaosState(ctx, h, state, iter)
}

func (s *chaosState) controlPlaneAvailable(iter int) bool {
	return s.controlPlaneDownUntil == 0 || iter > s.controlPlaneDownUntil
}

func (s *chaosState) randBetween(min int, max int) int {
	if max <= min {
		return min
	}
	return min + s.rng.Intn(max-min+1)
}

func (s *chaosState) randomActiveWorker() *chaosWorkerSlot {
	candidates := make([]*chaosWorkerSlot, 0)
	for _, slot := range s.workers {
		if slot != nil && slot.active && !slot.retired {
			candidates = append(candidates, slot)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates[s.rng.Intn(len(candidates))]
}

func (s *chaosState) randomRestartableWorker(iter int) *chaosWorkerSlot {
	candidates := make([]*chaosWorkerSlot, 0)
	for _, slot := range s.workers {
		if slot == nil || slot.retired || slot.active {
			continue
		}
		if slot.restartAt > iter {
			candidates = append(candidates, slot)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates[s.rng.Intn(len(candidates))]
}

func (s *chaosState) activeWorkers() int {
	count := 0
	for _, slot := range s.workers {
		if slot != nil && slot.active && !slot.retired {
			count++
		}
	}
	return count
}

func readChaosPositiveEnvInt(t *testing.T, key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if t != nil {
		require.NoErrorf(t, err, "invalid integer env %s=%q", key, raw)
		require.Greaterf(t, v, 0, "env %s must be > 0", key)
	} else {
		if err != nil || v <= 0 {
			return fallback
		}
	}
	return v
}

func readChaosInt64Env(t *testing.T, key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if t != nil {
		require.NoErrorf(t, err, "invalid int64 env %s=%q", key, raw)
	} else if err != nil {
		return fallback
	}
	return v
}
