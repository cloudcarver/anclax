//go:build smoke

package chaos

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func runTagConcurrencyFaultScenarios(t *testing.T, ctx context.Context, h *Harness) {
	t.Helper()
	q := querier.New(h.inspector.pool)
	user := h.User()
	gateKey := func(name string) string { return "chaos:gate:" + name }
	submit := func(t *testing.T, name string, labels, tags []string) int32 {
		t.Helper()
		id, err := user.SubmitStressProbe(ctx, SubmitStressProbeRequest{
			TaskName: "TAG-" + name, Group: gateKey(name), Labels: labels, Tags: tags,
		})
		require.NoError(t, err)
		t.Cleanup(func() { h.gateService.release(gateKey(name)) })
		return id
	}
	waitGate := func(t *testing.T, name string, attempts int) []GateAttempt {
		t.Helper()
		require.Eventually(t, func() bool {
			return len(h.gateService.snapshot(gateKey(name))) >= attempts
		}, 10*time.Second, 20*time.Millisecond, "executor never reached gate %s attempt %d", name, attempts)
		got := h.gateService.snapshot(gateKey(name))
		require.Len(t, got, attempts)
		require.False(t, got[attempts-1].Exited)
		return got
	}
	row := func(t *testing.T, id int32) *querier.AnclaxTask {
		t.Helper()
		r, err := q.GetTaskByID(ctx, id)
		require.NoError(t, err)
		return r
	}
	waitBlocked := func(t *testing.T, id int32, tag string, attempts int32) {
		t.Helper()
		require.Eventually(t, func() bool {
			r, err := q.GetTaskByID(ctx, id)
			return err == nil && r.ConcurrencyWaitTag != nil && *r.ConcurrencyWaitTag == tag && r.LockedAt == nil
		}, 10*time.Second, 20*time.Millisecond, "task %d never waited for %s", id, tag)
		r := row(t, id)
		require.Equal(t, attempts, r.Attempts)
		var permits int
		require.NoError(t, h.inspector.pool.QueryRow(ctx, "SELECT count(*) FROM anclax.task_tag_permits WHERE task_id=$1", id).Scan(&permits))
		require.Zero(t, permits)
	}
	usage := func(t *testing.T, tag string, want int32) {
		t.Helper()
		r, err := q.GetTaskTagConcurrency(ctx, tag)
		require.NoError(t, err)
		require.Equal(t, want, r.InUse, tag)
	}
	finish := func(t *testing.T, names ...string) {
		t.Helper()
		for _, name := range names {
			h.gateService.release(gateKey(name))
		}
		for _, name := range names {
			require.NoError(t, user.ExpectCompleted(ctx, "TAG-"+name, 10*time.Second))
		}
	}
	setLimit := func(t *testing.T, tag string, limit int32) {
		t.Helper()
		require.NoError(t, q.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: tag, MaxConcurrency: limit}))
	}
	assertTakeover := func(t *testing.T, old *querier.AnclaxTask, name, newWorker string, tags []string) {
		t.Helper()
		attempts := waitGate(t, name, 2)
		require.Equal(t, newWorker, attempts[1].Worker)
		r := row(t, old.ID)
		require.Equal(t, old.Attempts+1, r.Attempts)
		require.Equal(t, old.LeaseVersion+1, r.LeaseVersion)
		require.NotEqual(t, old.WorkerID, r.WorkerID)
		// A result from the old owner must not release the replacement's permits.
		_, err := q.FinalizeTaskAttempt(ctx, querier.FinalizeTaskAttemptParams{
			ID: old.ID, WorkerID: old.WorkerID, LeaseVersion: old.LeaseVersion, Status: "completed", Attempts: old.Attempts,
		})
		require.ErrorIs(t, err, pgx.ErrNoRows)
		for _, tag := range tags {
			usage(t, tag, 1)
		}
		var permitVersions []int64
		require.NoError(t, h.inspector.pool.QueryRow(ctx, "SELECT array_agg(lease_version ORDER BY tag) FROM anclax.task_tag_permits WHERE task_id=$1", old.ID).Scan(&permitVersions))
		require.Equal(t, []int64{r.LeaseVersion, r.LeaseVersion}, permitVersions)
		h.report.AddEvent("assert.tag_takeover", "TAG-"+name, "specific interrupted task reclaimed; stale owner fenced", map[string]any{
			"taskID": old.ID, "oldLeaseVersion": old.LeaseVersion, "newLeaseVersion": r.LeaseVersion, "attempts": r.Attempts, "newWorker": newWorker,
		})
	}

	t.Run("group_limit_wait_release_and_unlimited_progress", func(t *testing.T) {
		tags := []string{"chaos:concurrency:global", "chaos:concurrency:group:w1"}
		for _, name := range []string{"group-holder-a", "group-holder-b"} {
			submit(t, name, []string{"w1"}, tags)
			waitGate(t, name, 1)
		}
		usage(t, tags[0], 2)
		usage(t, tags[1], 2)
		id := submit(t, "group-waiter", []string{"w1"}, tags)
		waitBlocked(t, id, tags[1], 0)
		require.Empty(t, h.gateService.snapshot(gateKey("group-waiter")))
		_, err := user.SubmitStressProbe(ctx, SubmitStressProbeRequest{TaskName: "TAG-unlimited", Group: "default", Labels: []string{"w2"}})
		require.NoError(t, err)
		require.NoError(t, user.ExpectCompleted(ctx, "TAG-unlimited", 5*time.Second))
		usage(t, tags[1], 2)
		finish(t, "group-holder-a")
		waitGate(t, "group-waiter", 1)
		require.Equal(t, int32(1), row(t, id).Attempts)
		finish(t, "group-holder-b", "group-waiter")
		h.report.AddEvent("assert.tag_wait_release", tags[1], "full group blocked admission; unlimited task progressed; release admitted waiter", map[string]any{"waiter": id})
	})

	t.Run("global_limit_wait_and_release", func(t *testing.T) {
		for i, group := range []string{"w1", "w1", "w2"} {
			name := fmt.Sprintf("global-holder-%d", i)
			submit(t, name, []string{group}, []string{"chaos:concurrency:global", "chaos:concurrency:group:" + group})
			waitGate(t, name, 1)
		}
		usage(t, "chaos:concurrency:global", 3)
		id := submit(t, "global-waiter", []string{"w2"}, []string{"chaos:concurrency:global", "chaos:concurrency:group:w2"})
		waitBlocked(t, id, "chaos:concurrency:global", 0)
		finish(t, "global-holder-0")
		waitGate(t, "global-waiter", 1)
		require.Equal(t, int32(1), row(t, id).Attempts)
		finish(t, "global-holder-1", "global-holder-2", "global-waiter")
		h.report.AddEvent("assert.tag_wait_release", "chaos:concurrency:global", "full global limit blocked admission; release admitted waiter", map[string]any{"waiter": id})
	})

	t.Run("kill_lease_owner_and_change_limits_during_recovery", func(t *testing.T) {
		tags := []string{"chaos:concurrency:takeover:a", "chaos:concurrency:takeover:b"}
		for _, tag := range tags {
			setLimit(t, tag, 1)
		}
		require.NoError(t, h.StartWorker(ctx, "tag-owner", []string{"tag-takeover"}))
		t.Cleanup(func() { _ = h.StopWorker(ctx, "tag-owner", true) })
		require.NoError(t, h.WaitWorkerOnline(ctx, "tag-owner", true, 10*time.Second))
		id := submit(t, "takeover", []string{"tag-takeover"}, tags)
		waitGate(t, "takeover", 1)
		old := row(t, id)
		require.Equal(t, int32(1), old.Attempts)
		require.NoError(t, h.StopWorker(ctx, "tag-owner", true))
		for _, tag := range tags {
			setLimit(t, tag, 0)
		}
		require.NoError(t, h.StartWorker(ctx, "tag-replacement", []string{"tag-takeover"}))
		t.Cleanup(func() { _ = h.StopWorker(ctx, "tag-replacement", true) })
		require.NoError(t, h.WaitWorkerOnline(ctx, "tag-replacement", true, 10*time.Second))
		waitBlocked(t, id, tags[0], 1)
		for _, tag := range tags {
			usage(t, tag, 0)
		}
		require.NoError(t, q.RemoveTaskTagConcurrencyLimit(ctx, tags[0]))
		waitBlocked(t, id, tags[1], 1)
		setLimit(t, tags[1], 1)
		assertTakeover(t, old, "takeover", "tag-replacement", tags)
		finish(t, "takeover")
		for _, tag := range tags {
			usage(t, tag, 0)
		}
	})

	t.Run("database_partition_with_owner_process_alive", func(t *testing.T) {
		tags := []string{"chaos:concurrency:partition:a", "chaos:concurrency:partition:b"}
		for _, tag := range tags {
			setLimit(t, tag, 1)
		}
		proxy, err := newTCPProxy(fmt.Sprintf("127.0.0.1:%d", h.postgresHostPort))
		require.NoError(t, err)
		t.Cleanup(proxy.close)
		dsn := fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", h.cfg.PostgresUser, h.cfg.PostgresPassword, proxy.containerAddress(), h.cfg.PostgresDatabase)
		// Suppress heartbeat shutdown during this short scenario so lease renewal
		// must fence a live process independently of the worker heartbeat path.
		require.NoError(t, h.startWorker(ctx, "tag-partition-owner", []string{"tag-partition"}, map[string]string{
			"CHAOS_DSN": dsn, "CHAOS_HEARTBEAT_INTERVAL_MS": "3600000",
		}))
		t.Cleanup(func() { _ = h.StopWorker(ctx, "tag-partition-owner", true) })
		require.NoError(t, h.WaitWorkerOnline(ctx, "tag-partition-owner", true, 10*time.Second))
		container := prefixedName(h.cfg.RunID, "tag-partition-owner")
		processState := func() string {
			out, err := dockerCommand(ctx, "inspect", "--format", "{{.State.Running}} {{.State.StartedAt}} {{.RestartCount}}", container)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(string(out), "true "), string(out))
			return string(out)
		}
		before := processState()
		id := submit(t, "partition", []string{"tag-partition"}, tags)
		waitGate(t, "partition", 1)
		old := row(t, id)
		proxy.partition()
		h.report.AddEvent("chaos.worker_db_partition", container, "dropped existing and new database connections; process and gate connection remain alive", map[string]any{"taskID": id})
		require.Eventually(t, func() bool {
			attempts := h.gateService.snapshot(gateKey("partition"))
			return len(attempts) == 1 && attempts[0].Exited
		}, 5*time.Second, 20*time.Millisecond, "old handler did not stop after losing its lease")
		require.NoError(t, h.StartWorker(ctx, "tag-partition-replacement", []string{"tag-partition"}))
		t.Cleanup(func() { _ = h.StopWorker(ctx, "tag-partition-replacement", true) })
		require.NoError(t, h.WaitWorkerOnline(ctx, "tag-partition-replacement", true, 10*time.Second))
		assertTakeover(t, old, "partition", "tag-partition-replacement", tags)
		require.Equal(t, before, processState(), "partition must not restart or kill the old worker")
		proxy.heal()
		h.report.AddEvent("chaos.worker_db_heal", container, "restored database connectivity to the same process", nil)
		require.Eventually(t, func() bool {
			proxy.mu.Lock()
			defer proxy.mu.Unlock()
			return len(proxy.conns) > 0
		}, 5*time.Second, 20*time.Millisecond, "old worker did not reconnect after healing")
		require.Equal(t, before, processState())
		for _, tag := range tags {
			usage(t, tag, 1)
		}
		finish(t, "partition")
		require.Equal(t, int32(2), row(t, id).Attempts)
		for _, tag := range tags {
			usage(t, tag, 0)
		}
	})
}
