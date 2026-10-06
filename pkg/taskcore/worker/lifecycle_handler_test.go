package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type fakeTx struct{}

func (t *fakeTx) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (t *fakeTx) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, nil
}

func (t *fakeTx) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	return pgx.Row(nil)
}

func (t *fakeTx) Commit(context.Context) error {
	return nil
}

func (t *fakeTx) Rollback(context.Context) error {
	return nil
}

func TestDecideAttempt(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	retry := &apigen.TaskRetryPolicy{Interval: "1m", MaxAttempts: 3}
	cron := &apigen.TaskCronjob{CronExpression: "0 * * * * *"}
	boom := errors.New("boom")
	cases := []struct {
		name        string
		attrs       apigen.TaskAttributes
		attempts    int32
		execErr     error
		status      apigen.TaskStatus
		next        time.Duration
		count       int32
		event, hook bool
	}{
		{"completed", apigen.TaskAttributes{}, 1, nil, apigen.Completed, 0, 1, false, false},
		{"no retry policy", apigen.TaskAttributes{}, 1, boom, apigen.Failed, 0, 1, true, true},
		{"retry", apigen.TaskAttributes{RetryPolicy: retry}, 1, boom, apigen.Pending, time.Minute, 1, true, false},
		{"exhausted", apigen.TaskAttributes{RetryPolicy: retry}, 3, boom, apigen.Failed, 0, 3, true, true},
		{"fatal", apigen.TaskAttributes{RetryPolicy: retry}, 1, taskcore.ErrFatalTask, apigen.Failed, 0, 1, true, true},
		{"quiet retry", apigen.TaskAttributes{RetryPolicy: retry}, 1, taskcore.ErrRetryTaskWithoutErrorEvent, apigen.Pending, time.Minute, 1, false, false},
		{"paused", apigen.TaskAttributes{}, 1, taskcore.ErrTaskPaused, apigen.Paused, 0, 1, false, false},
		{"cancelled", apigen.TaskAttributes{}, 1, taskcore.ErrTaskCancelled, apigen.Cancelled, 0, 1, false, false},
		{"deferred", apigen.TaskAttributes{RetryPolicy: retry}, 3, taskcore.DeferTask(time.Second), apigen.Pending, time.Second, 2, false, false},
		{"shutdown", apigen.TaskAttributes{RetryPolicy: retry}, 3, taskcore.ErrTaskInterrupted, apigen.Pending, 0, 2, false, false},
		{"cron success resets budget", apigen.TaskAttributes{Cronjob: cron, RetryPolicy: retry}, 3, nil, apigen.Pending, time.Minute, 0, false, false},
		{"cron failure without retry", apigen.TaskAttributes{Cronjob: cron}, 1, boom, apigen.Pending, time.Minute, 0, true, true},
		{"cron failure retry", apigen.TaskAttributes{Cronjob: cron, RetryPolicy: retry}, 1, boom, apigen.Pending, time.Minute, 1, true, false},
		{"cron exhausted starts next occurrence", apigen.TaskAttributes{Cronjob: cron, RetryPolicy: retry}, 3, boom, apigen.Pending, time.Minute, 0, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := decideAttempt(Task{Attributes: tc.attrs, Attempts: tc.attempts}, tc.execErr, now)
			require.NoError(t, err)
			require.Equal(t, tc.status, out.status)
			require.Equal(t, tc.count, out.attempts)
			require.Equal(t, tc.event, out.errorEvent)
			require.Equal(t, tc.hook, out.failureHook)
			if tc.status == apigen.Pending {
				require.NotNil(t, out.startedAt)
				require.Equal(t, now.Add(tc.next), *out.startedAt)
			}
		})
	}
	_, err := decideAttempt(Task{}, taskcore.ErrTaskLockLost, now)
	require.ErrorIs(t, err, taskcore.ErrTaskLockLost)
	_, err = decideAttempt(Task{Attributes: apigen.TaskAttributes{RetryPolicy: &apigen.TaskRetryPolicy{Interval: "bad", MaxAttempts: 3}}}, boom, now)
	require.Error(t, err)
	_, err = decideAttempt(Task{Attributes: apigen.TaskAttributes{Cronjob: &apigen.TaskCronjob{CronExpression: "bad"}}}, nil, now)
	require.Error(t, err)
}

func TestFinalizeAttemptPreservesControlStateAndLease(t *testing.T) {
	for _, status := range []string{"cancelled", "paused"} {
		t.Run(status, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			m := model.NewMockModelInterfaceWithTransaction(ctrl)
			id := uuid.New()
			task := Task{ID: 7, LeaseVersion: 9, Attempts: 1}
			m.EXPECT().FinalizeTaskAttempt(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, p querier.FinalizeTaskAttemptParams) (string, error) {
				require.Equal(t, int64(9), p.LeaseVersion)
				require.Equal(t, id, p.WorkerID.UUID)
				return status, nil
			})
			h := NewTaskLifeCycleHandler(m, nil, id)
			require.NoError(t, h.FinalizeAttempt(context.Background(), &fakeTx{}, task, nil))
			// No completion event or failure hook is written for the stale outcome.
		})
	}
}

func TestFinalizeAttemptLostLease(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := model.NewMockModelInterfaceWithTransaction(ctrl)
	m.EXPECT().FinalizeTaskAttempt(gomock.Any(), gomock.Any()).Return("", pgx.ErrNoRows)
	h := NewTaskLifeCycleHandler(m, nil, uuid.New())
	require.ErrorIs(t, h.FinalizeAttempt(context.Background(), &fakeTx{}, Task{ID: 7}, nil), taskcore.ErrTaskLockLost)
}

func TestFinalizeAttemptWritesCompletionEvent(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := model.NewMockModelInterfaceWithTransaction(ctrl)
	m.EXPECT().FinalizeTaskAttempt(gomock.Any(), gomock.Any()).Return("completed", nil)
	m.EXPECT().InsertEvent(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, spec apigen.EventSpec) (*querier.AnclaxEvent, error) {
		require.Equal(t, apigen.TaskCompleted, spec.Type)
		require.Equal(t, int32(7), spec.TaskCompleted.TaskID)
		return &querier.AnclaxEvent{}, nil
	})
	require.NoError(t, NewTaskLifeCycleHandler(m, nil, uuid.New()).FinalizeAttempt(context.Background(), &fakeTx{}, Task{ID: 7}, nil))
}
