//go:build smoke

package taskcoree2e_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/asynctask"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func regressionPtr[T any](x T) *T { return &x }

type lookupBarrier struct {
	model.ModelInterface
	count atomic.Int32
	ready chan struct{}
}

func (b *lookupBarrier) GetTaskByUniqueTag(ctx context.Context, tag *string) (*querier.AnclaxTask, error) {
	task, err := b.ModelInterface.GetTaskByUniqueTag(ctx, tag)
	if errors.Is(err, pgx.ErrNoRows) {
		if b.count.Add(1) == 2 {
			close(b.ready)
		}
		select {
		case <-b.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return task, err
}

type sqlErrorHook struct{}

func (sqlErrorHook) HandleTask(context.Context, worker.Task) error { return errors.New("failed") }
func (sqlErrorHook) RegisterTaskHandler(worker.TaskHandler)        {}
func (sqlErrorHook) OnTaskFailed(ctx context.Context, tx core.Tx, _ worker.TaskSpec, _ int32) error {
	_, err := tx.Exec(ctx, "SELECT 1 / 0")
	return err
}

type panickingHandler struct{ sqlErrorHook }

func (panickingHandler) HandleTask(context.Context, worker.Task) error { panic("executor failed") }
func (panickingHandler) OnTaskFailed(context.Context, core.Tx, worker.TaskSpec, int32) error {
	panic("hook failed")
}

func TestTaskLifecycleRegressionsSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		t.Run("late_heartbeat_cannot_revive_offline_worker", func(t *testing.T) {
			runLateWorkerHeartbeatScenario(t, ctx, m)
		})
		s := store.NewTaskStore(m)
		reset := func(t *testing.T) {
			t.Helper()
			if err := m.RunTransactionWithTx(ctx, func(tx core.Tx, _ model.ModelInterface) error {
				_, err := tx.Exec(ctx, "TRUNCATE anclax.task_tag_concurrency, anclax.task_tags, anclax.task_tag_permits, anclax.tasks, anclax.events, anclax.workers, anclax.worker_runtime_configs RESTART IDENTITY")
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		enqueue := func(t *testing.T, attrs apigen.TaskAttributes) int32 {
			t.Helper()
			id, err := s.PushTask(ctx, &apigen.Task{Status: apigen.Pending, Spec: apigen.TaskSpec{Type: "review", Payload: []byte("{}")}, Attributes: attrs})
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		port := func(t *testing.T, h worker.TaskHandler) *worker.ModelPort {
			t.Helper()
			p, err := worker.NewModelPort(m, uuid.New(), nil, h, 9*time.Second, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			return p
		}

		t.Run("cancel_must_survive_late_success_finalization", func(t *testing.T) {
			reset(t)
			id := enqueue(t, apigen.TaskAttributes{})
			p := port(t, nil)
			task, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CancelTask(ctx, id); err != nil {
				t.Fatal(err)
			}
			if err := p.FinalizeTask(ctx, *task, nil); err != nil {
				t.Fatal(err)
			}
			row, err := m.GetTaskByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if row.Status != "cancelled" {
				t.Errorf("cancelled task became %q after late successful finalization", row.Status)
			}
		})

		t.Run("cron_failure_must_preserve_future_schedule", func(t *testing.T) {
			reset(t)
			id := enqueue(t, apigen.TaskAttributes{Cronjob: &apigen.TaskCronjob{CronExpression: "*/1 * * * * *"}})
			p := port(t, nil)
			task, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if err := p.FinalizeTask(ctx, *task, errors.New("transient service error")); err != nil {
				t.Fatal(err)
			}
			row, err := m.GetTaskByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if row.Status != "pending" {
				t.Errorf("cron task is %q after one failure, started_at=%v", row.Status, row.StartedAt)
			}
		})

		t.Run("pause_survives_late_result_and_resume_invalidates_old_attempt", func(t *testing.T) {
			reset(t)
			id := enqueue(t, apigen.TaskAttributes{})
			p := port(t, nil)
			attempt, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			require.NoError(t, err)
			require.NoError(t, s.PauseTask(ctx, id))
			require.NoError(t, p.FinalizeTask(ctx, *attempt, errors.New("late failure")))
			row, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "paused", row.Status)
			require.Nil(t, row.LockedAt)
			require.NoError(t, s.ResumeTask(ctx, id))
			attempt, err = p.ClaimByID(ctx, id, worker.ClaimRequest{})
			require.NoError(t, err)
			require.NoError(t, s.PauseTask(ctx, id))
			require.NoError(t, s.ResumeTask(ctx, id))
			require.NoError(t, p.FinalizeTask(ctx, *attempt, store.ErrTaskPaused))
			row, err = m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "pending", row.Status)
			require.Greater(t, row.LeaseVersion, attempt.LeaseVersion)
		})

		t.Run("same_worker_old_lease_cannot_renew_or_finalize_new_attempt", func(t *testing.T) {
			reset(t)
			id := enqueue(t, apigen.TaskAttributes{})
			p := port(t, nil)
			old, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			require.NoError(t, err)
			require.NoError(t, m.RunTransactionWithTx(ctx, func(tx core.Tx, _ model.ModelInterface) error {
				_, err := tx.Exec(ctx, "UPDATE anclax.tasks SET locked_at = statement_timestamp() - interval '10 seconds', lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1", id)
				return err
			}))
			current, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			require.NoError(t, err)
			require.Greater(t, current.LeaseVersion, old.LeaseVersion)
			row, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			_, err = m.RefreshTaskLock(ctx, querier.RefreshTaskLockParams{ID: id, WorkerID: row.WorkerID, LeaseVersion: old.LeaseVersion})
			require.ErrorIs(t, err, pgx.ErrNoRows)
			require.NoError(t, p.FinalizeTask(ctx, *old, nil))
			row, err = m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "pending", row.Status)
			require.NotNil(t, row.LockedAt)
			require.Equal(t, current.LeaseVersion, row.LeaseVersion)
			require.NoError(t, p.FinalizeTask(ctx, *current, nil))
			row, err = m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "completed", row.Status)
			require.NoError(t, s.PauseTask(ctx, id))
			require.NoError(t, s.CancelTask(ctx, id))
			require.NoError(t, s.ResumeTask(ctx, id))
			row, err = m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "completed", row.Status, "terminal state must be stable")
		})

		t.Run("cron_success_resets_retry_budget_for_next_occurrence", func(t *testing.T) {
			reset(t)
			id := enqueue(t, apigen.TaskAttributes{Cronjob: &apigen.TaskCronjob{CronExpression: "*/1 * * * * *"}, RetryPolicy: &apigen.TaskRetryPolicy{Interval: "1ms", MaxAttempts: 2}})
			p := port(t, nil)
			for i, result := range []error{errors.New("first failure"), nil, errors.New("next occurrence failure")} {
				require.NoError(t, m.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{ID: id, StartedAt: regressionPtr(time.Now().Add(-time.Second))}))
				attempt, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
				require.NoError(t, err)
				require.NoError(t, p.FinalizeTask(ctx, *attempt, result))
				row, err := m.GetTaskByID(ctx, id)
				require.NoError(t, err)
				require.Equal(t, "pending", row.Status)
				want := int32(1)
				if i == 1 {
					want = 0
				}
				require.Equal(t, want, row.Attempts)
			}
		})

		t.Run("runtime_config_request_is_idempotent_and_ack_never_regresses", func(t *testing.T) {
			reset(t)
			requestID := "stable-request"
			first, err := m.CreateWorkerRuntimeConfigForRequest(ctx, querier.CreateWorkerRuntimeConfigForRequestParams{RequestID: &requestID, Payload: []byte(`{"maxStrictPercentage":10}`)})
			require.NoError(t, err)
			second, err := m.CreateWorkerRuntimeConfig(ctx, []byte(`{"maxStrictPercentage":20}`))
			require.NoError(t, err)
			repeated, err := m.CreateWorkerRuntimeConfigForRequest(ctx, querier.CreateWorkerRuntimeConfigForRequestParams{RequestID: &requestID, Payload: []byte(`{"maxStrictPercentage":30}`)})
			require.NoError(t, err)
			require.Equal(t, first.Version, repeated.Version)
			require.JSONEq(t, string(first.Payload), string(repeated.Payload))
			latest, err := m.GetLatestWorkerRuntimeConfig(ctx)
			require.NoError(t, err)
			require.Equal(t, second.Version, latest.Version)
			workerID := uuid.New()
			_, err = m.UpsertWorker(ctx, querier.UpsertWorkerParams{ID: workerID, Labels: []byte(`[]`), AppliedConfigVersion: second.Version})
			require.NoError(t, err)
			row, err := m.UpsertWorker(ctx, querier.UpsertWorkerParams{ID: workerID, Labels: []byte(`[]`), AppliedConfigVersion: first.Version})
			require.NoError(t, err)
			require.Equal(t, second.Version, row.AppliedConfigVersion)
		})

		t.Run("concurrent_unique_enqueue_must_return_same_id", func(t *testing.T) {
			reset(t)
			b := &lookupBarrier{ModelInterface: m, ready: make(chan struct{})}
			bs := store.NewTaskStore(b)
			type result struct {
				id  int32
				err error
			}
			results := make(chan result, 2)
			for i := 0; i < 2; i++ {
				go func() {
					id, err := bs.PushTask(ctx, &apigen.Task{Status: apigen.Pending, UniqueTag: regressionPtr("same-tag"), Spec: apigen.TaskSpec{Type: "review", Payload: []byte("{}")}})
					results <- result{id, err}
				}()
			}
			a, bres := <-results, <-results
			if a.err != nil || bres.err != nil || a.id != bres.id {
				t.Errorf("concurrent enqueue results: (%d, %v), (%d, %v)", a.id, a.err, bres.id, bres.err)
			}
		})

		t.Run("hook_sql_error_must_not_rollback_terminal_status", func(t *testing.T) {
			reset(t)
			id := enqueue(t, apigen.TaskAttributes{})
			p := port(t, sqlErrorHook{})
			task, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			if err != nil {
				t.Fatal(err)
			}
			finalizeErr := p.FinalizeTask(ctx, *task, errors.New("business failure"))
			require.NoError(t, finalizeErr)
			row, err := m.GetTaskByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if row.Status != "failed" {
				t.Errorf("hook SQL failure leaves task %q, finalization error=%v", row.Status, finalizeErr)
			}
		})

		t.Run("executor_and_failure_hook_panics_preserve_terminal_outcome", func(t *testing.T) {
			reset(t)
			id := enqueue(t, apigen.TaskAttributes{})
			p := port(t, panickingHandler{})
			attempt, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			require.NoError(t, err)
			execErr := p.ExecuteTask(ctx, *attempt)
			require.ErrorContains(t, execErr, "executor failed")
			require.NoError(t, p.FinalizeTask(ctx, *attempt, execErr))
			row, err := m.GetTaskByID(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "failed", row.Status)
			require.Nil(t, row.LockedAt)
			require.False(t, p.TaskRuntimesActive([]int32{id}))
		})

		t.Run("nil_labels_and_multiple_weighted_labels_have_one_group", func(t *testing.T) {
			reset(t)
			id := enqueue(t, apigen.TaskAttributes{Labels: regressionPtr([]string(nil))})
			p := port(t, nil)
			attempt, err := p.ClaimNormalByGroup(ctx, worker.ClaimNormalRequest{Group: worker.DefaultWeightGroup})
			require.NoError(t, err)
			require.Equal(t, id, attempt.ID)
			require.NoError(t, p.FinalizeTask(ctx, *attempt, nil))
			id = enqueue(t, apigen.TaskAttributes{Labels: regressionPtr([]string{"b", "a"})})
			p, err = worker.NewModelPort(m, uuid.New(), []string{"a", "b"}, nil, time.Second, 0)
			require.NoError(t, err)
			_, err = p.ClaimNormalByGroup(ctx, worker.ClaimNormalRequest{Group: "b", WeightedLabels: []string{"a", "b"}})
			require.ErrorIs(t, err, worker.ErrNoTask)
			attempt, err = p.ClaimNormalByGroup(ctx, worker.ClaimNormalRequest{Group: "a", WeightedLabels: []string{"a", "b"}})
			require.NoError(t, err)
			require.Equal(t, id, attempt.ID)
		})

		t.Run("all_builtin_control_types_use_the_control_claim_lane", func(t *testing.T) {
			reset(t)
			types := []string{taskgen.BroadcastUpdateWorkerRuntimeConfig, taskgen.ApplyWorkerRuntimeConfigToWorker,
				taskgen.BroadcastCancelTask, taskgen.CancelTaskOnWorker, taskgen.BroadcastPauseTask, taskgen.PauseTaskOnWorker}
			for i, taskType := range types {
				require.True(t, worker.IsControlTask(taskType))
				_, err := s.PushTask(ctx, &apigen.Task{Status: apigen.Pending, Spec: apigen.TaskSpec{Type: taskType, Payload: []byte(`{}`)}, Attributes: apigen.TaskAttributes{Priority: regressionPtr(int32(i % 2))}})
				require.NoError(t, err)
			}
			p := port(t, nil)
			_, err := p.ClaimStrict(ctx, worker.ClaimRequest{})
			require.ErrorIs(t, err, worker.ErrNoTask)
			_, err = p.ClaimNormalByGroup(ctx, worker.ClaimNormalRequest{Group: worker.DefaultWeightGroup})
			require.ErrorIs(t, err, worker.ErrNoTask)
			var claimed []string
			for range types {
				attempt, err := p.ClaimControl(ctx, worker.ClaimRequest{})
				require.NoError(t, err)
				claimed = append(claimed, attempt.GetType())
			}
			require.ElementsMatch(t, types, claimed)
		})

		t.Run("claim_must_skip_locked_head_when_other_task_ready", func(t *testing.T) {
			reset(t)
			first := enqueue(t, apigen.TaskAttributes{Priority: regressionPtr(int32(10))})
			second := enqueue(t, apigen.TaskAttributes{Priority: regressionPtr(int32(9))})
			claimed := make(chan error, 1)
			release := make(chan struct{})
			finished := make(chan error, 1)
			go func() {
				finished <- m.RunTransactionWithTx(ctx, func(_ core.Tx, txm model.ModelInterface) error {
					task, err := txm.ClaimStrictTask(ctx, querier.ClaimStrictTaskParams{WorkerID: uuid.NullUUID{UUID: uuid.New(), Valid: true}, LockTtlMs: 9000})
					if err != nil {
						claimed <- err
						return err
					}
					if task.ID != first {
						t.Errorf("wrong initial claim %d", task.ID)
					}
					claimed <- nil
					<-release
					return nil
				})
			}()
			defer func() {
				close(release)
				if err := <-finished; err != nil {
					t.Error(err)
				}
			}()
			if err := <-claimed; err != nil {
				t.Fatal(err)
			}
			deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer deadlineCancel()
			task, err := port(t, nil).ClaimStrict(deadlineCtx, worker.ClaimRequest{})
			if err != nil {
				t.Errorf("claim blocked on task %d despite ready task %d: %v", first, second, err)
				return
			}
			if task.ID != second {
				t.Errorf("claimed %d, want unlocked task %d", task.ID, second)
			}
		})

		t.Run("normal_fallback_rechecks_new_strict_work_with_reserved_capacity", func(t *testing.T) {
			reset(t)
			p := port(t, nil)
			_, err := p.ClaimStrict(ctx, worker.ClaimRequest{})
			require.ErrorIs(t, err, worker.ErrNoTask)
			strict := enqueue(t, apigen.TaskAttributes{Priority: regressionPtr(int32(9))})
			normal := enqueue(t, apigen.TaskAttributes{})
			attempt, err := p.ClaimNormalByGroup(ctx, worker.ClaimNormalRequest{ClaimRequest: worker.ClaimRequest{AllowStrict: true}, Group: worker.DefaultWeightGroup})
			require.NoError(t, err)
			require.Equal(t, strict, attempt.ID)
			enqueue(t, apigen.TaskAttributes{Priority: regressionPtr(int32(10))})
			attempt, err = p.ClaimNormalByGroup(ctx, worker.ClaimNormalRequest{Group: worker.DefaultWeightGroup})
			require.NoError(t, err)
			require.Equal(t, normal, attempt.ID, "normal-only capacity must not exceed the strict cap")
		})

		t.Run("control_fanout_must_progress_with_concurrency_one", func(t *testing.T) {
			reset(t)
			g := globalctx.New()
			runner := taskgen.NewTaskRunner(s)
			workerIDs := []uuid.UUID{uuid.New(), uuid.New()}
			components := make([]*worker.WorkerComponents, 0, 2)
			parentIDs := make([]int32, 0, 2)
			for _, workerID := range workerIDs {
				cfg := &config.Config{Worker: config.Worker{WorkerID: regressionPtr(workerID.String()), Concurrency: regressionPtr(1), MaxStrictPercentage: regressionPtr(0), PollInterval: regressionPtr(10 * time.Millisecond)}}
				executor := asynctask.NewExecutor(cfg, m, runner)
				handler := taskgen.NewTaskHandler(executor)
				c, err := worker.BuildWorkerComponents(cfg, m, handler)
				if err != nil {
					t.Fatal(err)
				}
				facade, err := worker.NewWorker(g, c, handler)
				if err != nil {
					t.Fatal(err)
				}
				facade.RegisterTaskHandler(asynctask.NewWorkerControlTaskHandler(facade))
				executor.SetLocalWorker(facade)
				if err := c.Port.RegisterWorker(ctx, workerID.String(), c.Engine.Labels(), 0); err != nil {
					t.Fatal(err)
				}
				components = append(components, c)
			}
			defer func() {
				g.Cancel()
				drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
				defer drainCancel()
				for _, c := range components {
					_ = c.Port.WaitTaskRuntimes(drainCtx, parentIDs)
					c.Runtime.Close()
				}
			}()
			// Seed both parent broadcasts before either starts. Each targets the other worker.
			for _, requestID := range []string{"review-a", "review-b"} {
				id, err := runner.RunBroadcastCancelTask(ctx, &taskgen.BroadcastCancelTaskParameters{
					TaskIDs: []int32{999}, WorkerIDs: workerIDs, RequestID: regressionPtr(requestID), AckPollInterval: regressionPtr("10ms"),
				}, store.WithPriority(0))
				if err != nil {
					t.Fatal(err)
				}
				parentIDs = append(parentIDs, id)
			}

			stopped := make([]chan struct{}, len(components))
			for i, c := range components {
				stopped[i] = make(chan struct{})
				go func(c *worker.WorkerComponents, done chan struct{}) { defer close(done); c.Runtime.Start(g.Context()) }(c, stopped[i])
			}
			defer func() {
				g.Cancel()
				for _, done := range stopped {
					<-done
				}
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				completed := 0
				for _, id := range parentIDs {
					row, err := m.GetTaskByID(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					if row.Status == "completed" {
						completed++
					}
				}
				if completed == len(parentIDs) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("broadcast parents failed to converge with concurrency=1")
				}
				time.Sleep(10 * time.Millisecond)
			}
			for _, id := range parentIDs {
				children, err := m.ListTaskDescendantIDs(ctx, &id)
				if err != nil {
					t.Fatal(err)
				}
				// A deferred parent can move to the other worker. Each target still
				// gets at most one durable command across all parent executions.
				require.NotEmpty(t, children)
				require.LessOrEqual(t, len(children), len(workerIDs))
				targets := map[string]bool{}
				for _, childID := range children {
					child, err := m.GetTaskByID(ctx, childID)
					require.NoError(t, err)
					require.NotNil(t, child.Attributes.Labels)
					target := (*child.Attributes.Labels)[0]
					require.False(t, targets[target], "duplicate child for the same worker")
					targets[target] = true
				}
				parent, err := m.GetTaskByID(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if parent.Attempts != 1 {
					t.Fatalf("deferrals consumed retry attempts: %d", parent.Attempts)
				}
			}

		})

	})
}
