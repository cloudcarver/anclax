//go:build smoke

package taskcoree2e_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/taskcore/ctrl"
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

func runUniqueTagReleaseRegressions(t *testing.T, ctx context.Context, m model.ModelInterface) {
	s := store.NewTaskStore(m)
	newTask := func(tag string, attrs apigen.TaskAttributes) *apigen.Task {
		return &apigen.Task{
			Status: apigen.Pending, UniqueTag: &tag, Attributes: attrs,
			Spec: apigen.TaskSpec{Type: "uniqueTagRelease", Payload: []byte(`{}`)},
		}
	}
	enqueue := func(t *testing.T, tag string, attrs apigen.TaskAttributes, overrides ...store.TaskOverride) int32 {
		t.Helper()
		task := newTask(tag, attrs)
		for _, override := range overrides {
			require.NoError(t, override(task))
		}
		id, err := s.PushTask(ctx, task)
		require.NoError(t, err)
		return id
	}
	newPort := func(t *testing.T, handler worker.TaskHandler) *worker.ModelPort {
		t.Helper()
		p, err := worker.NewModelPort(m, uuid.New(), nil, handler, 9*time.Second, 3*time.Second)
		require.NoError(t, err)
		return p
	}
	check := func(t *testing.T, id int32, tag string, status apigen.TaskStatus, released bool) {
		t.Helper()
		row, err := s.GetTaskByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, status, row.Status)
		if released {
			require.Nil(t, row.UniqueTag)
			_, err := s.GetTaskByUniqueTag(ctx, tag)
			require.ErrorIs(t, err, store.ErrTaskNotFound)
		} else {
			require.Equal(t, &tag, row.UniqueTag)
			found, err := s.GetTaskByUniqueTag(ctx, tag)
			require.NoError(t, err)
			require.Equal(t, id, found.ID)
		}
		// The existing owner's policy decides deduplication, even if a new
		// submission uses a different release policy.
		next := enqueue(t, tag, apigen.TaskAttributes{}, store.WithReleaseUniqueTagOnTerminal(!released))
		if released {
			require.NotEqual(t, id, next)
		} else {
			require.Equal(t, id, next)
		}
	}

	retry := &apigen.TaskRetryPolicy{Interval: "1m", MaxAttempts: 3}
	cron := &apigen.TaskCronjob{CronExpression: "0 * * * * *"}
	for _, tc := range []struct {
		name     string
		release  *bool
		attrs    apigen.TaskAttributes
		execErr  error
		handler  worker.TaskHandler
		status   apigen.TaskStatus
		released bool
	}{
		{name: "default_completed", status: apigen.Completed},
		{name: "default_failed", execErr: errors.New("failure"), status: apigen.Failed},
		{name: "default_cancelled", execErr: store.ErrTaskCancelled, status: apigen.Cancelled},
		{name: "explicit_false", release: regressionPtr(false), status: apigen.Completed},
		{name: "completed", release: regressionPtr(true), status: apigen.Completed, released: true},
		{name: "failed", release: regressionPtr(true), execErr: errors.New("failure"), status: apigen.Failed, released: true},
		{name: "retries_exhausted", release: regressionPtr(true), attrs: apigen.TaskAttributes{RetryPolicy: &apigen.TaskRetryPolicy{Interval: "1m", MaxAttempts: 1}}, execErr: errors.New("failure"), status: apigen.Failed, released: true},
		{name: "fatal", release: regressionPtr(true), attrs: apigen.TaskAttributes{RetryPolicy: retry}, execErr: store.ErrFatalTask, status: apigen.Failed, released: true},
		{name: "cancelled", release: regressionPtr(true), execErr: store.ErrTaskCancelled, status: apigen.Cancelled, released: true},
		{name: "failure_hook_error", release: regressionPtr(true), execErr: errors.New("failure"), handler: sqlErrorHook{}, status: apigen.Failed, released: true},
		{name: "retry", release: regressionPtr(true), attrs: apigen.TaskAttributes{RetryPolicy: retry}, execErr: errors.New("retry"), status: apigen.Pending},
		{name: "deferred", release: regressionPtr(true), execErr: store.DeferTask(time.Minute), status: apigen.Pending},
		{name: "shutdown", release: regressionPtr(true), execErr: store.ErrTaskInterrupted, status: apigen.Pending},
		{name: "paused", release: regressionPtr(true), execErr: store.ErrTaskPaused, status: apigen.Paused},
		{name: "cron_success", release: regressionPtr(true), attrs: apigen.TaskAttributes{Cronjob: cron}, status: apigen.Pending},
		{name: "cron_failure", release: regressionPtr(true), attrs: apigen.TaskAttributes{Cronjob: cron}, execErr: errors.New("failure"), handler: sqlErrorHook{}, status: apigen.Pending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tag := uuid.NewString()
			attrs := tc.attrs
			attrs.ReleaseUniqueTagOnTerminal = tc.release
			id := enqueue(t, tag, attrs)
			p := newPort(t, tc.handler)
			attempt, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			require.NoError(t, err)
			require.NoError(t, p.FinalizeTask(ctx, *attempt, tc.execErr))
			check(t, id, tag, tc.status, tc.released)
		})
	}

	t.Run("pause_resume_and_stale_attempt_retain_tag", func(t *testing.T) {
		tag := uuid.NewString()
		id := enqueue(t, tag, apigen.TaskAttributes{}, store.WithReleaseUniqueTagOnTerminal(true))
		p := newPort(t, nil)
		attempt, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
		require.NoError(t, err)
		require.NoError(t, s.PauseTask(ctx, id))
		require.NoError(t, p.FinalizeTask(ctx, *attempt, nil))
		check(t, id, tag, apigen.Paused, false)
		require.NoError(t, s.ResumeTask(ctx, id))
		require.NoError(t, p.FinalizeTask(ctx, *attempt, nil))
		check(t, id, tag, apigen.Pending, false)
	})

	t.Run("cancel_and_late_result_cannot_release_new_owner", func(t *testing.T) {
		tag := uuid.NewString()
		id := enqueue(t, tag, apigen.TaskAttributes{}, store.WithReleaseUniqueTagOnTerminal(true))
		p := newPort(t, nil)
		attempt, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
		require.NoError(t, err)
		require.NoError(t, s.CancelTask(ctx, id))
		next := enqueue(t, tag, apigen.TaskAttributes{}, store.WithReleaseUniqueTagOnTerminal(true))
		require.NotEqual(t, id, next)
		require.NoError(t, p.FinalizeTask(ctx, *attempt, nil))
		old, err := s.GetTaskByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, apigen.Cancelled, old.Status)
		require.Nil(t, old.UniqueTag)
		check(t, next, tag, apigen.Pending, false)
	})

	for _, transition := range []string{"finalize", "cancel"} {
		t.Run(transition+"_rollback_restores_tag", func(t *testing.T) {
			tag := uuid.NewString()
			id := enqueue(t, tag, apigen.TaskAttributes{}, store.WithReleaseUniqueTagOnTerminal(true))
			workerID := uuid.New()
			p, err := worker.NewModelPort(m, workerID, nil, nil, 9*time.Second, 3*time.Second)
			require.NoError(t, err)
			attempt, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
			require.NoError(t, err)
			before, err := s.GetTaskByID(ctx, id)
			require.NoError(t, err)
			rollback := errors.New("rollback terminal transition")
			err = m.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
				if transition == "cancel" {
					require.NoError(t, s.CancelTaskWithTx(ctx, tx, id))
				} else {
					h := worker.NewTaskLifeCycleHandler(m, nil, workerID)
					require.NoError(t, h.FinalizeAttempt(ctx, tx, *attempt, nil))
				}
				row, err := txm.GetTaskByID(ctx, id)
				require.NoError(t, err)
				require.Nil(t, row.UniqueTag)
				next, err := s.PushTaskWithTx(ctx, tx, newTask(tag, apigen.TaskAttributes{}))
				require.NoError(t, err)
				require.NotEqual(t, id, next)
				return rollback
			})
			require.ErrorIs(t, err, rollback)
			check(t, id, tag, before.Status, false)
		})
	}

	t.Run("cancel_cascades_with_each_tasks_own_policy", func(t *testing.T) {
		tags := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
		parent := enqueue(t, tags[0], apigen.TaskAttributes{}, store.WithReleaseUniqueTagOnTerminal(true))
		child := enqueue(t, tags[1], apigen.TaskAttributes{}, store.WithParentTaskID(parent), store.WithReleaseUniqueTagOnTerminal(true))
		retained := enqueue(t, tags[2], apigen.TaskAttributes{}, store.WithParentTaskID(child))
		require.NoError(t, s.PauseTask(ctx, child))
		control := ctrl.NewWorkerControlPlane(m, taskgen.NewTaskRunner(s), s, nil)
		require.NoError(t, control.CancelTask(ctx, parent))
		check(t, parent, tags[0], apigen.Cancelled, true)
		check(t, child, tags[1], apigen.Cancelled, true)
		check(t, retained, tags[2], apigen.Cancelled, false)
	})

	t.Run("worker_status_update_respects_lease_and_releases_tag", func(t *testing.T) {
		tag := uuid.NewString()
		id := enqueue(t, tag, apigen.TaskAttributes{}, store.WithReleaseUniqueTagOnTerminal(true))
		workerID := uuid.New()
		p, err := worker.NewModelPort(m, workerID, nil, nil, 9*time.Second, 3*time.Second)
		require.NoError(t, err)
		attempt, err := p.ClaimByID(ctx, id, worker.ClaimRequest{})
		require.NoError(t, err)
		params := querier.UpdateTaskStatusByWorkerParams{
			ID: id, WorkerID: uuid.NullUUID{UUID: workerID, Valid: true},
			LeaseVersion: attempt.LeaseVersion - 1, Status: string(apigen.Completed),
		}
		_, err = m.UpdateTaskStatusByWorker(ctx, params)
		require.ErrorIs(t, err, pgx.ErrNoRows)
		before, err := s.GetTaskByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, &tag, before.UniqueTag)
		params.LeaseVersion = attempt.LeaseVersion
		_, err = m.UpdateTaskStatusByWorker(ctx, params)
		require.NoError(t, err)
		check(t, id, tag, apigen.Completed, true)
	})

	t.Run("concurrent_reenqueue_after_release_returns_one_new_id", func(t *testing.T) {
		tag := uuid.NewString()
		id := enqueue(t, tag, apigen.TaskAttributes{}, store.WithReleaseUniqueTagOnTerminal(true))
		require.NoError(t, s.CancelTask(ctx, id))
		barrier := &lookupBarrier{ModelInterface: m, ready: make(chan struct{})}
		concurrentStore := store.NewTaskStore(barrier)
		type result struct {
			id  int32
			err error
		}
		results := make(chan result, 2)
		deadlineCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		for range 2 {
			go func() {
				id, err := concurrentStore.PushTask(deadlineCtx, newTask(tag, apigen.TaskAttributes{}))
				results <- result{id, err}
			}()
		}
		first, second := <-results, <-results
		require.NoError(t, first.err)
		require.NoError(t, second.err)
		require.NotEqual(t, id, first.id)
		require.Equal(t, first.id, second.id)
	})
}
