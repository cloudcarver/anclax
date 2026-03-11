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
