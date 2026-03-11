//go:build smoke
// +build smoke

package taskcoree2e_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/stretchr/testify/require"
)

const (
	defaultLongevitySeed            int64 = 424242
	defaultLongevityIterations            = 24
	defaultLongevityEnqueueBatch          = 4
	defaultLongevityTaskSleepMs           = 250
	defaultLongevityInFlightWaitMs        = 100
	defaultLongevityInterIterWaitMs       = 120
)

type longevityConfig struct {
	seed            int64
	iterations      int
	enqueueBatch    int
	taskSleepMs     int32
	inFlightWaitMs  int32
	interIterWaitMs int32
}

type longevityWorkerSlot struct {
	name      string
	labels    []string
	useDSN    bool
	active    bool
	retired   bool
	restartAt int
}

type longevityState struct {
	rng                   *rand.Rand
	workers               []*longevityWorkerSlot
	expected              map[string]string
	nextWorkerID          int
	nextControlTaskID     int
	controlPlaneDownUntil int
	workerDisruptions     int
	lastPostgresRestart   int
}

func newLongevityState(seed int64) *longevityState {
	return &longevityState{
		rng: rand.New(rand.NewSource(seed)),
		workers: []*longevityWorkerSlot{
			{name: "long_w1_a", labels: []string{"w1"}, useDSN: true},
			{name: "long_w2_a", labels: []string{"w2"}, useDSN: true},
			{name: "long_any_a", labels: []string{"w1", "w2"}, useDSN: true},
			{name: "long_any_b", labels: []string{"w1", "w2"}, useDSN: true},
		},
		expected:          map[string]string{},
		nextWorkerID:      1,
		nextControlTaskID: 1,
	}
}

// TestTaskcoreLongevityChaosSmoke is a seeded-random long-running chaos smoke test.
//
// Goals covered by this test:
// - workers go down randomly and may stay down for a long time
// - some workers are retired forever, with replacements joining later
// - Postgres restarts randomly during load
// - the control plane becomes unavailable for random windows
// - once capacity returns, all expected tasks converge to terminal states
//
// Replay knobs:
// - ANCLAX_TASKCORE_LONGEVITY_SEED
// - ANCLAX_TASKCORE_LONGEVITY_ITERATIONS
// - ANCLAX_TASKCORE_LONGEVITY_ENQUEUE_BATCH
// - ANCLAX_TASKCORE_LONGEVITY_TASK_SLEEP_MS
func TestTaskcoreLongevityChaosSmoke(t *testing.T) {
	cfg := readLongevityConfig(t)
	state := newLongevityState(cfg.seed)
	start := time.Now()

	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		env, err := newDSTEnv(m)
		require.NoError(t, err)
		t.Cleanup(func() { _ = env.runtime.stopAllWorkers(ctx, 5*time.Second) })

		t.Logf("longevity seed=%d iterations=%d enqueueBatch=%d taskSleepMs=%d", cfg.seed, cfg.iterations, cfg.enqueueBatch, cfg.taskSleepMs)
		require.NoError(t, startInitialLongevityWorkers(ctx, env, state))

		for iter := 1; iter <= cfg.iterations; iter++ {
			require.NoError(t, applyControlPlaneAvailability(env, state, iter))
			require.NoError(t, reconcileLongevityWorkers(ctx, env, state, iter))
			require.NoError(t, enqueueLongevityBatch(ctx, env, state, iter, cfg))
			require.NoError(t, env.runtime.SleepMs(ctx, cfg.inFlightWaitMs))

			desc, err := runRandomLongevityChaos(t, ctx, env, state, iter, cfg)
			require.NoError(t, err)
			t.Logf("longevity iter=%d action=%s activeWorkers=%d expectedTasks=%d", iter, desc, state.activeWorkerCount(), len(state.expected))

			require.NoError(t, env.runtime.SleepMs(ctx, cfg.interIterWaitMs))
		}

		require.NoError(t, restoreLongevityCluster(ctx, env, state, cfg.iterations+1))
		env.controlPlane.SetAvailable(true)

		require.NoError(t, waitForLongevityExpectations(ctx, env, state.expected, 90*time.Second))
		require.NoError(t, env.runtime.WaitNoPendingTasks(ctx, 30000))
		assertNoPendingRunningControlTasks(t, ctx, env)
		assertNoPendingOrRunningNamedPrefix(t, ctx, env, "LONG_")
		assertLongevityExpectationsMatch(t, ctx, env, state.expected)
		assertLongevityTakeoverObserved(t, ctx, env, state)

		t.Logf("longevity complete duration=%s expectedTasks=%d workerDisruptions=%d", time.Since(start), len(state.expected), state.workerDisruptions)
	})
}

func readLongevityConfig(t *testing.T) longevityConfig {
	t.Helper()
	return longevityConfig{
		seed:            readInt64Env(t, "ANCLAX_TASKCORE_LONGEVITY_SEED", defaultLongevitySeed),
		iterations:      readPositiveEnvInt(t, "ANCLAX_TASKCORE_LONGEVITY_ITERATIONS", defaultLongevityIterations),
		enqueueBatch:    readPositiveEnvInt(t, "ANCLAX_TASKCORE_LONGEVITY_ENQUEUE_BATCH", defaultLongevityEnqueueBatch),
		taskSleepMs:     int32(readPositiveEnvInt(t, "ANCLAX_TASKCORE_LONGEVITY_TASK_SLEEP_MS", defaultLongevityTaskSleepMs)),
		inFlightWaitMs:  int32(readPositiveEnvInt(t, "ANCLAX_TASKCORE_LONGEVITY_IN_FLIGHT_WAIT_MS", defaultLongevityInFlightWaitMs)),
		interIterWaitMs: int32(readPositiveEnvInt(t, "ANCLAX_TASKCORE_LONGEVITY_INTER_ITER_WAIT_MS", defaultLongevityInterIterWaitMs)),
	}
}

func readPositiveEnvInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	require.NoErrorf(t, err, "invalid integer env %s=%q", key, raw)
	require.Greaterf(t, v, 0, "env %s must be > 0", key)
	return v
}

func readInt64Env(t *testing.T, key string, fallback int64) int64 {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	require.NoErrorf(t, err, "invalid int64 env %s=%q", key, raw)
	return v
}

func startInitialLongevityWorkers(ctx context.Context, env *dstEnv, state *longevityState) error {
	for _, slot := range state.workers {
		if err := startLongevityWorkerSlot(ctx, env, slot); err != nil {
			return err
		}
	}
	return nil
}

func startLongevityWorkerSlot(ctx context.Context, env *dstEnv, slot *longevityWorkerSlot) error {
	if slot == nil || slot.retired {
		return nil
	}
	if err := env.runtime.StartWorker(ctx, slot.name, "noop", "noop", slot.labels, 10, 20, 200, 20, 2, 50, slot.useDSN, ""); err != nil {
		return err
	}
	slot.active = true
	slot.restartAt = 0
	return nil
}

func stopLongevityWorkerSlot(ctx context.Context, env *dstEnv, slot *longevityWorkerSlot) error {
	if slot == nil || !slot.active {
		return nil
	}
	if err := env.runtime.StopWorker(ctx, slot.name); err != nil {
		return err
	}
	slot.active = false
	return nil
}

func applyControlPlaneAvailability(env *dstEnv, state *longevityState, iter int) error {
	env.controlPlane.SetAvailable(state.controlPlaneAvailable(iter))
	return nil
}

func reconcileLongevityWorkers(ctx context.Context, env *dstEnv, state *longevityState, iter int) error {
	for _, slot := range state.workers {
		if slot == nil || slot.retired || slot.active {
			continue
		}
		if slot.restartAt == 0 || slot.restartAt > iter {
			continue
		}
		if err := startLongevityWorkerSlot(ctx, env, slot); err != nil {
			return err
		}
	}
	return nil
}

func enqueueLongevityBatch(ctx context.Context, env *dstEnv, state *longevityState, iter int, cfg longevityConfig) error {
	for batch := 0; batch < cfg.enqueueBatch; batch++ {
		name := fmt.Sprintf("LONG_JOB_I%03d_T%02d", iter, batch)
		labels, group := longevityTaskLabelsAndGroup(state.rng.Intn(3))
		payload := fmt.Sprintf(`{"jobID":%d,"sleepMs":%d,"group":%q}`,
			iter*1000+batch,
			cfg.taskSleepMs,
			group,
		)
		if err := env.taskStore.EnqueueRaw(ctx, name, "stressProbe", payload, 0, int32(state.randBetween(1, 4)), labels, "", 0, "", 0); err != nil {
			return err
		}
		state.expected[name] = "completed"
	}
	return nil
}

func longevityTaskLabelsAndGroup(n int) ([]string, string) {
	switch n {
	case 1:
		return []string{"w1"}, "w1"
	case 2:
		return []string{"w2"}, "w2"
	default:
		return []string{}, "default"
	}
}

func runRandomLongevityChaos(t *testing.T, ctx context.Context, env *dstEnv, state *longevityState, iter int, cfg longevityConfig) (string, error) {
	t.Helper()
	actions := []func(*testing.T, context.Context, *dstEnv, *longevityState, int, longevityConfig) (string, bool, error){
		longevityActionWorkerTemporaryDown,
		longevityActionWorkerRetireAndReplace,
		longevityActionControlPlaneOutage,
		longevityActionRuntimeConfigUpdate,
		longevityActionPostgresRestart,
		longevityActionWorkerRejoinEarly,
	}
	start := state.rng.Intn(len(actions))
	for i := 0; i < len(actions); i++ {
		action := actions[(start+i)%len(actions)]
		desc, applied, err := action(t, ctx, env, state, iter, cfg)
		if err != nil {
			return "", err
		}
		if applied {
			return desc, nil
		}
	}
	return "no-op", nil
}

func longevityActionWorkerTemporaryDown(_ *testing.T, ctx context.Context, env *dstEnv, state *longevityState, iter int, _ longevityConfig) (string, bool, error) {
	slot := state.randomActiveWorker()
	if slot == nil {
		return "", false, nil
	}
	if err := stopLongevityWorkerSlot(ctx, env, slot); err != nil {
		return "", false, err
	}
	slot.restartAt = iter + state.randBetween(4, 9)
	state.workerDisruptions++
	return fmt.Sprintf("worker-down name=%s restartAt=%d", slot.name, slot.restartAt), true, nil
}

func longevityActionWorkerRetireAndReplace(_ *testing.T, ctx context.Context, env *dstEnv, state *longevityState, iter int, _ longevityConfig) (string, bool, error) {
	slot := state.randomActiveWorker()
	if slot == nil {
		return "", false, nil
	}
	if err := stopLongevityWorkerSlot(ctx, env, slot); err != nil {
		return "", false, err
	}
	slot.retired = true
	replacement := state.addReplacementWorker(slot.labels, iter+state.randBetween(3, 7))
	state.workerDisruptions++
	return fmt.Sprintf("worker-retired name=%s replacement=%s replacementAt=%d", slot.name, replacement.name, replacement.restartAt), true, nil
}

func longevityActionControlPlaneOutage(_ *testing.T, _ context.Context, env *dstEnv, state *longevityState, iter int, _ longevityConfig) (string, bool, error) {
	if !state.controlPlaneAvailable(iter) {
		return "", false, nil
	}
	duration := state.randBetween(3, 6)
	state.controlPlaneDownUntil = iter + duration
	env.controlPlane.SetAvailable(false)
	return fmt.Sprintf("control-plane-down untilIter=%d", state.controlPlaneDownUntil), true, nil
}

func longevityActionRuntimeConfigUpdate(_ *testing.T, ctx context.Context, env *dstEnv, state *longevityState, iter int, _ longevityConfig) (string, bool, error) {
	if !state.controlPlaneAvailable(iter) || state.activeWorkerCount() == 0 {
		return "", false, nil
	}
	cfgKey := fmt.Sprintf("LONG_CFG_%03d", iter)
	maxStrict := int32(state.randBetween(0, 60))
	w1Weight := int32(state.randBetween(1, 4))
	w2Weight := int32(state.randBetween(1, 4))
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := env.controlPlane.UpdateRuntimeConfig(callCtx, cfgKey, maxStrict, 1, w1Weight, w2Weight); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("runtime-config key=%s strict=%d w1=%d w2=%d", cfgKey, maxStrict, w1Weight, w2Weight), true, nil
}

func longevityActionPauseOrCancelDelayedTask(_ *testing.T, ctx context.Context, env *dstEnv, state *longevityState, iter int, cfg longevityConfig) (string, bool, error) {
	if !state.controlPlaneAvailable(iter) {
		return "", false, nil
	}
	verb := "pause"
	expectedStatus := "paused"
	if state.rng.Intn(2) == 0 {
		verb = "cancel"
		expectedStatus = "cancelled"
	}
	name := fmt.Sprintf("LONG_CTRL_I%03d_%02d", iter, state.nextControlTaskID)
	state.nextControlTaskID++
	labels, group := longevityTaskLabelsAndGroup(1 + state.rng.Intn(2))
	payload := fmt.Sprintf(`{"jobID":%d,"sleepMs":%d,"group":%q}`,
		iter*1000+900+state.nextControlTaskID,
		cfg.taskSleepMs,
		group,
	)
	if err := env.taskStore.EnqueueRaw(ctx, name, "stressProbe", payload, 0, 1, labels, "", 0, "", 3); err != nil {
		return "", false, err
	}
	state.expected[name] = "completed"

	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var err error
	switch verb {
	case "pause":
		err = env.controlPlane.PauseTask(callCtx, name)
	default:
		err = env.controlPlane.CancelTask(callCtx, name)
	}
	if err != nil {
		return "", false, err
	}
	state.expected[name] = expectedStatus
	return fmt.Sprintf("%s-delayed-task name=%s labels=%v", verb, name, labels), true, nil
}

func longevityActionPostgresRestart(t *testing.T, ctx context.Context, env *dstEnv, state *longevityState, iter int, _ longevityConfig) (string, bool, error) {
	if state.lastPostgresRestart != 0 && iter-state.lastPostgresRestart < 4 {
		return "", false, nil
	}
	if err := runDocker(t, "stop", smokeContainerName); err != nil {
		return "", false, err
	}
	if err := runDocker(t, "start", smokeContainerName); err != nil {
		return "", false, err
	}
	if err := waitForPostgres(t, smokePostgresDSN(), 20*time.Second); err != nil {
		return "", false, err
	}

	affected := 0
	for _, slot := range state.workers {
		if slot == nil || slot.retired || !slot.active {
			continue
		}
		affected++
		slot.active = false
		if state.rng.Intn(100) < 25 {
			slot.retired = true
			state.addReplacementWorker(slot.labels, iter+state.randBetween(3, 7))
		} else {
			slot.restartAt = iter + state.randBetween(3, 8)
		}
		_ = env.runtime.MarkWorkerOffline(ctx, slot.name)
	}
	state.workerDisruptions++
	state.lastPostgresRestart = iter
	return fmt.Sprintf("postgres-restart affectedWorkers=%d", affected), true, nil
}

func longevityActionWorkerRejoinEarly(_ *testing.T, ctx context.Context, env *dstEnv, state *longevityState, iter int, _ longevityConfig) (string, bool, error) {
	slot := state.randomRestartableDownWorker(iter)
	if slot == nil {
		return "", false, nil
	}
	if err := startLongevityWorkerSlot(ctx, env, slot); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("worker-rejoin-early name=%s", slot.name), true, nil
}

func restoreLongevityCluster(ctx context.Context, env *dstEnv, state *longevityState, iter int) error {
	state.controlPlaneDownUntil = 0
	env.controlPlane.SetAvailable(true)
	for _, slot := range state.workers {
		if slot == nil || slot.retired || slot.active {
			continue
		}
		slot.restartAt = iter
	}
	return reconcileLongevityWorkers(ctx, env, state, iter)
}

func waitForLongevityExpectations(ctx context.Context, env *dstEnv, expected map[string]string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		matched, err := longevityExpectationsReached(ctx, env, expected)
		if err == nil && matched {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for longevity expectations")
}

func longevityExpectationsReached(ctx context.Context, env *dstEnv, expected map[string]string) (bool, error) {
	names := mapKeys(expected)
	if len(names) == 0 {
		return true, nil
	}
	rows, err := env.validator.Query(ctx, `
		select spec->'payload'->>'name' as task_name, status
		from anclax.tasks
		where spec->'payload'->>'name' = any($1::text[])
	`, []any{names})
	if err != nil {
		return false, err
	}
	if len(rows) != len(expected) {
		return false, nil
	}
	statuses := make(map[string]string, len(rows))
	for _, row := range rows {
		if len(row) != 2 {
			return false, fmt.Errorf("unexpected row width: %d", len(row))
		}
		statuses[row[0].(string)] = row[1].(string)
	}
	for name, want := range expected {
		if statuses[name] != want {
			return false, nil
		}
	}
	return true, nil
}

func assertLongevityExpectationsMatch(t *testing.T, ctx context.Context, env *dstEnv, expected map[string]string) {
	t.Helper()
	matched, err := longevityExpectationsReached(ctx, env, expected)
	if err == nil && matched {
		return
	}
	rows, qerr := env.validator.Query(ctx, `
		select spec->'payload'->>'name', status, attempts
		from anclax.tasks
		where spec->'payload'->>'name' like 'LONG_%'
		order by 1
	`, []any{})
	require.NoError(t, qerr)
	require.NoError(t, err)
	require.Truef(t, matched, "longevity expectations mismatch; rows=%v", rows)
}

func assertLongevityTakeoverObserved(t *testing.T, ctx context.Context, env *dstEnv, state *longevityState) {
	t.Helper()
	if state.workerDisruptions == 0 {
		return
	}
	rows, err := env.validator.Query(ctx, `
		select count(*)
		from anclax.tasks
		where spec->'payload'->>'name' like 'LONG_%'
		  and attempts >= 2
	`, []any{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Len(t, rows[0], 1)
	require.Greaterf(t, rows[0][0].(int64), int64(0), "expected takeover evidence after worker/postgres disruptions")
}

func assertNoPendingOrRunningNamedPrefix(t *testing.T, ctx context.Context, env *dstEnv, prefix string) {
	t.Helper()
	rows, err := env.validator.Query(ctx, `
		select count(*)
		from anclax.tasks
		where spec->'payload'->>'name' like $1
		  and status in ('pending', 'running')
	`, []any{prefix + "%"})
	require.NoError(t, err)
	require.Equal(t, [][]any{{int64(0)}}, rows)
}

func mapKeys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (s *longevityState) randBetween(min int, max int) int {
	if max <= min {
		return min
	}
	return min + s.rng.Intn(max-min+1)
}

func (s *longevityState) activeWorkerCount() int {
	count := 0
	for _, slot := range s.workers {
		if slot != nil && slot.active && !slot.retired {
			count++
		}
	}
	return count
}

func (s *longevityState) controlPlaneAvailable(iter int) bool {
	return s.controlPlaneDownUntil == 0 || iter > s.controlPlaneDownUntil
}

func (s *longevityState) randomActiveWorker() *longevityWorkerSlot {
	active := make([]*longevityWorkerSlot, 0)
	for _, slot := range s.workers {
		if slot != nil && slot.active && !slot.retired {
			active = append(active, slot)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return active[s.rng.Intn(len(active))]
}

func (s *longevityState) randomRestartableDownWorker(iter int) *longevityWorkerSlot {
	candidates := make([]*longevityWorkerSlot, 0)
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

func (s *longevityState) addReplacementWorker(labels []string, restartAt int) *longevityWorkerSlot {
	name := fmt.Sprintf("long_repl_%02d", s.nextWorkerID)
	s.nextWorkerID++
	slot := &longevityWorkerSlot{
		name:      name,
		labels:    append([]string(nil), labels...),
		useDSN:    true,
		restartAt: restartAt,
	}
	s.workers = append(s.workers, slot)
	return slot
}
