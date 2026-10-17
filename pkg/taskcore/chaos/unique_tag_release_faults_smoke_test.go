//go:build smoke

package chaos

import (
	"context"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func runUniqueTagReleaseFaultScenarios(t *testing.T, ctx context.Context, h *Harness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	q := querier.New(h.inspector.pool)
	user := h.User()
	enabled, disabled := true, false
	request := func(name, label string) SubmitStressProbeRequest {
		return SubmitStressProbeRequest{
			TaskName: "UNIQUE-" + name, UniqueTag: "UNIQUE-" + name,
			Group: "chaos:gate:unique:" + name, Labels: []string{label},
			ReleaseUniqueTagOnTerminal: &enabled,
		}
	}
	submit := func(t *testing.T, req SubmitStressProbeRequest) int32 {
		t.Helper()
		t.Cleanup(func() { h.gateService.release(req.Group) })
		id, err := user.SubmitStressProbe(ctx, req)
		require.NoError(t, err)
		return id
	}
	start := func(t *testing.T, name, label string) {
		t.Helper()
		require.NoError(t, h.StartWorker(ctx, name, []string{label}))
		t.Cleanup(func() { _ = h.StopWorker(context.Background(), name, true) })
		require.NoError(t, h.WaitWorkerOnline(ctx, name, true, 10*time.Second))
	}
	row := func(t *testing.T, id int32) *querier.AnclaxTask {
		t.Helper()
		r, err := q.GetTaskByID(ctx, id)
		require.NoError(t, err)
		return r
	}
	waitGate := func(t *testing.T, req SubmitStressProbeRequest, attempts int) []GateAttempt {
		t.Helper()
		require.Eventually(t, func() bool {
			return len(h.gateService.snapshot(req.Group)) >= attempts
		}, 10*time.Second, 20*time.Millisecond, "task %s did not reach gate", req.TaskName)
		got := h.gateService.snapshot(req.Group)
		require.Len(t, got, attempts)
		require.False(t, got[attempts-1].Exited)
		return got
	}
	terminal := func(t *testing.T, id int32, status string) *querier.AnclaxTask {
		t.Helper()
		got, err := user.WaitForTask(ctx, id, 10*time.Second)
		require.NoError(t, err)
		require.Equal(t, status, got)
		return row(t, id)
	}
	staleResult := func(t *testing.T, old *querier.AnclaxTask) {
		t.Helper()
		_, err := q.FinalizeTaskAttempt(ctx, querier.FinalizeTaskAttemptParams{
			ID: old.ID, WorkerID: old.WorkerID, LeaseVersion: old.LeaseVersion,
			Status: "completed", Attempts: old.Attempts,
		})
		require.ErrorIs(t, err, pgx.ErrNoRows)
	}

	t.Run("terminal_policy_and_concurrent_reuse", func(t *testing.T) {
		start(t, "unique-terminal", "unique-terminal")
		for _, policy := range []struct {
			name    string
			release *bool
		}{{"default", nil}, {"disabled", &disabled}, {"enabled", &enabled}} {
			for _, status := range []string{"completed", "failed", "cancelled"} {
				t.Run(policy.name+"_"+status, func(t *testing.T) {
					req := request(policy.name+"-"+status, "unique-terminal")
					req.ReleaseUniqueTagOnTerminal = policy.release
					if status == "failed" {
						req.FailMode = "always"
					}
					id := submit(t, req)
					waitGate(t, req, 1)
					require.Equal(t, id, submit(t, req), "running task must deduplicate")
					if status == "cancelled" {
						require.NoError(t, user.CancelTask(ctx, req.UniqueTag))
					} else {
						h.gateService.release(req.Group)
					}
					old := terminal(t, id, status)
					nextReq := request(policy.name+"-"+status+"-next", "unique-terminal")
					nextReq.UniqueTag = req.UniqueTag
					if policy.release == nil || !*policy.release {
						require.Equal(t, &req.UniqueTag, old.UniqueTag)
						require.Equal(t, id, submit(t, nextReq), "incoming policy must not change existing ownership")
						return
					}
					require.Nil(t, old.UniqueTag)
					// Race to acquire the released tag. The winner stays at the gate
					// until all requests resolve, preventing another valid generation.
					t.Cleanup(func() { h.gateService.release(nextReq.Group) })
					type result struct {
						id  int32
						err error
					}
					results := make(chan result, 8)
					barrier := make(chan struct{})
					for range cap(results) {
						go func() {
							<-barrier
							id, err := user.SubmitStressProbe(ctx, nextReq)
							results <- result{id, err}
						}()
					}
					close(barrier)
					var next int32
					for range cap(results) {
						got := <-results
						require.NoError(t, got.err)
						if next == 0 {
							next = got.id
						}
						require.Equal(t, next, got.id)
					}
					require.NotEqual(t, id, next)
					waitGate(t, nextReq, 1)
					h.gateService.release(nextReq.Group)
					require.Nil(t, terminal(t, next, "completed").UniqueTag)
					require.Nil(t, row(t, id).UniqueTag)
					h.report.AddEvent("assert.unique_tag_reuse", req.UniqueTag, "terminal owner released tag; concurrent requests resolved to one new owner", map[string]any{
						"oldTaskID": id, "newTaskID": next, "terminalStatus": status, "concurrentSubmissions": cap(results),
					})
				})
			}
		}
	})

	t.Run("killed_owner_retains_tag_until_replacement_finishes", func(t *testing.T) {
		start(t, "unique-owner", "unique-takeover")
		req := request("takeover", "unique-takeover")
		id := submit(t, req)
		waitGate(t, req, 1)
		old := row(t, id)
		require.NoError(t, h.StopWorker(ctx, "unique-owner", true))
		require.Equal(t, id, submit(t, req), "process death is not a terminal task transition")
		require.Equal(t, &req.UniqueTag, row(t, id).UniqueTag)
		start(t, "unique-replacement", "unique-takeover")
		attempts := waitGate(t, req, 2)
		require.Equal(t, "unique-replacement", attempts[1].Worker)
		replacement := row(t, id)
		require.Equal(t, old.Attempts+1, replacement.Attempts)
		require.Equal(t, old.LeaseVersion+1, replacement.LeaseVersion)
		staleResult(t, old)
		require.Equal(t, &req.UniqueTag, row(t, id).UniqueTag)
		require.Equal(t, id, submit(t, req))
		h.gateService.release(req.Group)
		require.Nil(t, terminal(t, id, "completed").UniqueTag)
		nextReq := request("takeover-next", "unique-takeover")
		nextReq.UniqueTag = req.UniqueTag
		next := submit(t, nextReq)
		require.NotEqual(t, id, next)
		waitGate(t, nextReq, 1)
		staleResult(t, old)
		staleResult(t, replacement)
		require.Equal(t, &req.UniqueTag, row(t, next).UniqueTag)
		require.Equal(t, next, submit(t, nextReq))
		h.gateService.release(nextReq.Group)
		require.Nil(t, terminal(t, next, "completed").UniqueTag)
		h.report.AddEvent("assert.unique_tag_takeover", req.UniqueTag, "killed owner's task kept its tag through takeover; late results could not release either replacement", map[string]any{
			"oldTaskID": id, "newTaskID": next, "oldLeaseVersion": old.LeaseVersion, "newLeaseVersion": replacement.LeaseVersion,
		})
	})

	t.Run("postgres_and_control_plane_restart_preserve_ownership_and_release", func(t *testing.T) {
		start(t, "unique-db-owner", "unique-db")
		finishedReq := request("db-finished", "unique-db")
		finishedID := submit(t, finishedReq)
		waitGate(t, finishedReq, 1)
		h.gateService.release(finishedReq.Group)
		require.Nil(t, terminal(t, finishedID, "completed").UniqueTag)
		activeReq := request("db-active", "unique-db")
		activeID := submit(t, activeReq)
		waitGate(t, activeReq, 1)
		old := row(t, activeID)
		require.NoError(t, h.StopControlPlane(ctx))
		require.NoError(t, h.RestartPostgres(ctx))
		// Recreate the interrupted worker explicitly; a failed heartbeat may
		// already have shut it down during the database outage.
		require.NoError(t, h.StopWorker(ctx, "unique-db-owner", true))
		require.NoError(t, h.StartControlPlane(ctx))
		require.Nil(t, row(t, finishedID).UniqueTag)
		require.Equal(t, &activeReq.UniqueTag, row(t, activeID).UniqueTag)
		require.Equal(t, activeID, submit(t, activeReq))
		nextReq := request("db-finished-next", "unique-db")
		nextReq.UniqueTag = finishedReq.UniqueTag
		next := submit(t, nextReq)
		require.NotEqual(t, finishedID, next)
		start(t, "unique-db-replacement", "unique-db")
		attempts := waitGate(t, activeReq, 2)
		require.Equal(t, "unique-db-replacement", attempts[1].Worker)
		waitGate(t, nextReq, 1)
		replacement := row(t, activeID)
		require.Greater(t, replacement.LeaseVersion, old.LeaseVersion)
		staleResult(t, old)
		require.Equal(t, &activeReq.UniqueTag, row(t, activeID).UniqueTag)
		h.gateService.release(activeReq.Group)
		h.gateService.release(nextReq.Group)
		require.Nil(t, terminal(t, activeID, "completed").UniqueTag)
		require.Nil(t, terminal(t, next, "completed").UniqueTag)
		h.report.AddEvent("assert.unique_tag_restart", activeReq.UniqueTag, "database and control-plane restart preserved both live ownership and committed release", map[string]any{
			"interruptedTaskID": activeID, "oldLeaseVersion": old.LeaseVersion, "newLeaseVersion": replacement.LeaseVersion,
			"oldTaskID": finishedID, "newTaskID": next,
		})
	})
}
