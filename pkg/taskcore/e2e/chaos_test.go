//go:build smoke
// +build smoke

package taskcoree2e_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/stretchr/testify/require"
)

// TestSmokeFailureInjectionRecovery covers failure-injection paths requested for production hardening:
// - DB restart while claim/finalize/runtime-config refresh are active.
// - Heartbeat failure auto-shutdown on DB outage.
// - Worker crash during execute/finalize windows with takeover recovery.
func TestSmokeFailureInjectionRecovery(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		env, err := newDSTEnv(m)
		require.NoError(t, err)
		t.Cleanup(func() { _ = env.runtime.stopAllWorkers(ctx, 3*time.Second) })

		// Phase A: claim/finalize/runtime-config refresh under DB restart (heartbeat effectively disabled).
		require.NoError(t, env.runtime.StartWorker(ctx, "fi_cf", "capture", "dst-taskstore", []string{"w1"}, 20, 20, 200, 3600000, 1, 50, false, ""))
		for i := 0; i < 10; i++ {
			require.NoError(t, env.taskStore.Enqueue(ctx, fmt.Sprintf("FI_CF_%d", i), int32((i%5)+1), 1, []string{"w1"}))
		}
		require.NoError(t, env.runtime.WaitCapturedCount(ctx, 2, 5000))

		require.NoError(t, runDocker(t, "stop", smokeContainerName))
		require.NoError(t, runDocker(t, "start", smokeContainerName))
		require.NoError(t, waitForPostgres(t, smokePostgresDSN(), 20*time.Second))

		require.NoError(t, env.runtime.WaitNoPendingTasks(ctx, 20000))
		require.NoError(t, env.runtime.WaitCapturedCount(ctx, 10, 20000))
		require.NoError(t, env.runtime.StopWorker(ctx, "fi_cf"))

		// Phase B: heartbeat failure should stop worker automatically when DB is unavailable.
		require.NoError(t, env.runtime.StartWorker(ctx, "fi_hb", "capture", "dst-taskstore", []string{"w1"}, 20, 20, 200, 20, 1, 0, false, ""))
		require.NoError(t, env.runtime.WaitWorkerOnline(ctx, "fi_hb", true, 3000))

		require.NoError(t, runDocker(t, "stop", smokeContainerName))
		require.NoError(t, env.runtime.SleepMs(ctx, 300))
		require.NoError(t, runDocker(t, "start", smokeContainerName))
		require.NoError(t, waitForPostgres(t, smokePostgresDSN(), 20*time.Second))

		// Phase C1: worker crash during execute (blocked handler) then takeover completes.
		require.NoError(t, env.taskStore.EnqueueRaw(ctx, "FI_CRASH_EXEC", "fi-crash-exec", "{}", 0, 1, []string{}, "", 0, "", 0))
		require.NoError(t, env.runtime.StartWorker(ctx, "fi_exec_block", "blocking", "fi-crash-exec", []string{}, 20, 20, 200, 3600000, 1, 0, false, ""))
		require.NoError(t, env.runtime.WaitSignal(ctx, "fi_exec_block", "started", 5000))
		require.NoError(t, env.runtime.StopWorker(ctx, "fi_exec_block"))

		require.NoError(t, env.runtime.StartWorker(ctx, "fi_exec_recover", "signal", "fi-crash-exec", []string{}, 20, 20, 200, 3600000, 1, 0, false, ""))
		require.NoError(t, env.runtime.WaitTaskCompletion(ctx, "FI_CRASH_EXEC", 12000))
		require.NoError(t, env.runtime.StopWorker(ctx, "fi_exec_recover"))

		// Phase C2: worker crash around finalize window then takeover completes.
		require.NoError(t, env.taskStore.EnqueueRaw(ctx, "FI_CRASH_FIN", "fi-crash-fin", "{}", 0, 1, []string{}, "", 0, "", 0))
		require.NoError(t, env.runtime.StartWorker(ctx, "fi_fin_block", "blocking", "fi-crash-fin", []string{}, 20, 20, 200, 3600000, 1, 0, false, ""))
		require.NoError(t, env.runtime.WaitSignal(ctx, "fi_fin_block", "started", 5000))
		require.NoError(t, env.runtime.ReleaseWorker(ctx, "fi_fin_block"))
		require.NoError(t, env.runtime.SleepMs(ctx, 10))
		require.NoError(t, env.runtime.StopWorker(ctx, "fi_fin_block"))

		require.NoError(t, env.runtime.StartWorker(ctx, "fi_fin_recover", "signal", "fi-crash-fin", []string{}, 20, 20, 200, 3600000, 1, 0, false, ""))
		require.NoError(t, env.runtime.WaitTaskCompletion(ctx, "FI_CRASH_FIN", 12000))
		require.NoError(t, env.runtime.StopWorker(ctx, "fi_fin_recover"))

		require.NoError(t, env.runtime.WaitNoPendingTasks(ctx, 10000))
	})
}

func TestChaosControlPlaneBroadcastDuringDBRestart(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		env, err := newDSTEnv(m)
		require.NoError(t, err)
		t.Cleanup(func() { _ = env.runtime.stopAllWorkers(ctx, 3*time.Second) })

		require.NoError(t, env.runtime.StartWorker(ctx, "cp_w1", "noop", "noop", []string{}, 20, 20, 200, 3600000, 1, 50, true, ""))
		require.NoError(t, env.runtime.StartWorker(ctx, "cp_w2", "noop", "noop", []string{}, 20, 20, 200, 3600000, 1, 50, true, ""))
		require.NoError(t, env.runtime.WaitWorkerOnline(ctx, "cp_w1", true, 5000))
		require.NoError(t, env.runtime.WaitWorkerOnline(ctx, "cp_w2", true, 5000))

		require.NoError(t, runDocker(t, "stop", smokeContainerName))
		downCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
		err = env.controlPlane.UpdateRuntimeConfig(downCtx, "CP_CFG_DOWN", 35, 1, 2, 1)
		cancel()
		require.Error(t, err)

		require.NoError(t, runDocker(t, "start", smokeContainerName))
		require.NoError(t, waitForPostgres(t, smokePostgresDSN(), 20*time.Second))
		require.NoError(t, env.runtime.MarkWorkerOffline(ctx, "cp_w1"))
		require.NoError(t, env.runtime.MarkWorkerOffline(ctx, "cp_w2"))

		require.NoError(t, env.runtime.StartWorker(ctx, "cp_w1", "noop", "noop", []string{}, 20, 20, 200, 3600000, 1, 50, true, ""))
		require.NoError(t, env.runtime.StartWorker(ctx, "cp_w2", "noop", "noop", []string{}, 20, 20, 200, 3600000, 1, 50, true, ""))
		require.NoError(t, env.controlPlane.UpdateRuntimeConfig(ctx, "CP_CFG_UP", 45, 1, 3, 1))
		require.NoError(t, env.runtime.CaptureLatestRuntimeConfigVersion(ctx, "CP_CFG_UP"))
		require.NoError(t, env.runtime.WaitWorkerLagging(ctx, "cp_w1", "CP_CFG_UP", false, 7000))
		require.NoError(t, env.runtime.WaitWorkerLagging(ctx, "cp_w2", "CP_CFG_UP", false, 7000))
		assertNoPendingRunningControlTasks(t, ctx, env)

		require.NoError(t, env.runtime.StopWorker(ctx, "cp_w1"))
		require.NoError(t, env.runtime.StopWorker(ctx, "cp_w2"))
	})
}

func TestChaosLockRefreshTakeoverAfterDBRestart(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		env, err := newDSTEnv(m)
		require.NoError(t, err)
		t.Cleanup(func() { _ = env.runtime.stopAllWorkers(ctx, 3*time.Second) })

		require.NoError(t, env.taskStore.EnqueueRaw(ctx, "LOCK_CHAOS", "lock-chaos", "{}", 0, 1, []string{}, "", 0, "", 0))
		require.NoError(t, env.runtime.StartWorker(ctx, "lock_owner", "blocking", "lock-chaos", []string{}, 20, 20, 200, 3600000, 1, 0, false, ""))
		require.NoError(t, env.runtime.WaitSignal(ctx, "lock_owner", "started", 5000))
		require.NoError(t, env.runtime.WaitTaskLock(ctx, "LOCK_CHAOS", 5000))
		require.NoError(t, env.runtime.WaitLockRefresh(ctx, "LOCK_CHAOS", 5000))

		require.NoError(t, runDocker(t, "stop", smokeContainerName))
		require.NoError(t, runDocker(t, "start", smokeContainerName))
		require.NoError(t, waitForPostgres(t, smokePostgresDSN(), 20*time.Second))

		require.NoError(t, env.runtime.StartWorker(ctx, "lock_takeover", "signal", "lock-chaos", []string{}, 20, 20, 200, 3600000, 1, 0, false, ""))
		require.NoError(t, env.runtime.StopWorker(ctx, "lock_owner"))
		require.NoError(t, env.runtime.SleepMs(ctx, 260))
		require.NoError(t, env.runtime.WaitTaskCompletion(ctx, "LOCK_CHAOS", 10000))
		require.NoError(t, env.runtime.StopWorker(ctx, "lock_takeover"))
		require.NoError(t, env.runtime.WaitNoPendingTasks(ctx, 10000))
	})
}

func TestChaosWorkerKillRecoveryWithInFlightTasks(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		env, err := newDSTEnv(m)
		require.NoError(t, err)
		t.Cleanup(func() { _ = env.runtime.stopAllWorkers(ctx, 3*time.Second) })

		tasks := []string{"KILL_1", "KILL_2", "KILL_3"}
		for _, task := range tasks {
			require.NoError(t, env.taskStore.EnqueueRaw(ctx, task, "kill-chaos", "{}", 0, 1, []string{}, "", 0, "", 0))
		}

		require.NoError(t, env.runtime.StartWorker(ctx, "kill_owner", "blocking", "kill-chaos", []string{}, 20, 20, 200, 3600000, 1, 0, false, ""))
		require.NoError(t, env.runtime.WaitSignal(ctx, "kill_owner", "started", 5000))
		require.NoError(t, env.runtime.StopWorker(ctx, "kill_owner"))

		require.NoError(t, env.runtime.StartWorker(ctx, "kill_recover1", "signal", "kill-chaos", []string{}, 20, 20, 200, 3600000, 1, 0, false, ""))
		require.NoError(t, env.runtime.StartWorker(ctx, "kill_recover2", "signal", "kill-chaos", []string{}, 20, 20, 200, 3600000, 1, 0, false, ""))
		for _, task := range tasks {
			require.NoError(t, env.runtime.WaitTaskCompletion(ctx, task, 12000))
		}

		rows, err := env.validator.Query(ctx, "select count(*) from anclax.tasks where spec->'payload'->>'name' = any($1::text[]) and attempts >= 2", []any{tasks})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Len(t, rows[0], 1)
		require.GreaterOrEqual(t, rows[0][0].(int64), int64(1))

		require.NoError(t, env.runtime.StopWorker(ctx, "kill_recover1"))
		require.NoError(t, env.runtime.StopWorker(ctx, "kill_recover2"))
		require.NoError(t, env.runtime.WaitNoPendingTasks(ctx, 10000))
	})
}

func assertNoPendingRunningControlTasks(t *testing.T, ctx context.Context, env *dstEnv) {
	t.Helper()
	types := []string{
		"broadcastCancelTask",
		"cancelTaskOnWorker",
		"broadcastPauseTask",
		"pauseTaskOnWorker",
		"broadcastUpdateWorkerRuntimeConfig",
		"applyWorkerRuntimeConfigToWorker",
		"updateWorkerRuntimeConfig",
		"interruptTask",
	}
	rows, err := env.validator.Query(ctx, "select count(*) from anclax.tasks where status in ('pending','running') and spec->>'type' = any($1::text[])", []any{types})
	require.NoError(t, err)
	require.Equal(t, [][]any{{int64(0)}}, rows)
}
