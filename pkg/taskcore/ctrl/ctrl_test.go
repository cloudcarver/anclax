package ctrl

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cloudcarver/anclax/core"
	tasklistener "github.com/cloudcarver/anclax/pkg/taskcore/listener"
	"github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type fakeTx struct{}

func taskTagsQueryParams(tags []string, exceptTagSets ...[]string) querier.ListTaskIDsByTagsParams {
	sets := make([][]string, 0, len(exceptTagSets))
	sets = append(sets, exceptTagSets...)
	raw, err := json.Marshal(sets)
	if err != nil {
		panic(err)
	}
	return querier.ListTaskIDsByTagsParams{
		Tags:          tags,
		ExceptTagSets: raw,
	}
}

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

type fakeTaskEventListener struct {
	t    testing.TB
	wait func(ctx context.Context, taskID int32) (<-chan tasklistener.TaskTerminalEvent, error)
}

func (f *fakeTaskEventListener) WaitTask(ctx context.Context, taskID int32) (<-chan tasklistener.TaskTerminalEvent, error) {
	if f.wait == nil {
		f.t.Fatalf("unexpected WaitTask call for task %d", taskID)
	}
	return f.wait(ctx, taskID)
}

func unexpectedTaskEventListener(t testing.TB) tasklistener.TaskEventListener {
	t.Helper()
	return &fakeTaskEventListener{t: t}
}

func completedTaskEventListener(t testing.TB, expectedTaskID int32) tasklistener.TaskEventListener {
	t.Helper()
	return &fakeTaskEventListener{
		t: t,
		wait: func(ctx context.Context, taskID int32) (<-chan tasklistener.TaskTerminalEvent, error) {
			require.Equal(t, expectedTaskID, taskID)
			ch := make(chan tasklistener.TaskTerminalEvent, 1)
			ch <- tasklistener.TaskTerminalEvent{TaskID: taskID, Status: apigen.Completed}
			close(ch)
			return ch, nil
		},
	}
}

func errorTaskEventListener(t testing.TB, expectedTaskID int32, err error) tasklistener.TaskEventListener {
	t.Helper()
	return &fakeTaskEventListener{
		t: t,
		wait: func(ctx context.Context, taskID int32) (<-chan tasklistener.TaskTerminalEvent, error) {
			require.Equal(t, expectedTaskID, taskID)
			return nil, err
		},
	}
}

func terminalTaskEventListener(t testing.TB, expectedTaskID int32, status apigen.TaskStatus) tasklistener.TaskEventListener {
	t.Helper()
	return &fakeTaskEventListener{
		t: t,
		wait: func(ctx context.Context, taskID int32) (<-chan tasklistener.TaskTerminalEvent, error) {
			require.Equal(t, expectedTaskID, taskID)
			ch := make(chan tasklistener.TaskTerminalEvent, 1)
			ch <- tasklistener.TaskTerminalEvent{TaskID: taskID, Status: status}
			close(ch)
			return ch, nil
		},
	}
}

func TestWaitForTaskCompleted(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(100)
	cp := NewWorkerControlPlane(
		model.NewMockModelInterface(ctrl),
		taskgen.NewMockTaskRunner(ctrl),
		store.NewMockTaskStoreInterface(ctrl),
		terminalTaskEventListener(t, taskID, apigen.Completed),
	)

	require.NoError(t, cp.WaitForTask(ctx, taskID))
}

func TestWaitForTaskFailedReturnsTaskError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(101)
	mockModel := model.NewMockModelInterface(ctrl)

	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(&querier.AnclaxTask{
		ID:         taskID,
		Attempts:   2,
		Attributes: apigen.TaskAttributes{RetryPolicy: &apigen.TaskRetryPolicy{MaxAttempts: 3}},
		Spec:       apigen.TaskSpec{Type: "demo-task"},
	}, nil)
	mockModel.EXPECT().GetLastTaskErrorEvent(ctx, taskID).Return(&querier.AnclaxEvent{
		Spec: apigen.EventSpec{
			Type: apigen.TaskError,
			TaskError: &apigen.EventTaskError{
				TaskID: taskID,
				Error:  "boom",
			},
		},
	}, nil)

	cp := NewWorkerControlPlane(
		mockModel,
		taskgen.NewMockTaskRunner(ctrl),
		store.NewMockTaskStoreInterface(ctrl),
		terminalTaskEventListener(t, taskID, apigen.Failed),
	)

	err := cp.WaitForTask(ctx, taskID)
	require.Error(t, err)
	require.ErrorContains(t, err, "task demo-task failed")
	require.ErrorContains(t, err, "id=101")
	require.ErrorContains(t, err, "attempts=2")
	require.ErrorContains(t, err, "max_attempts=3")
	require.ErrorContains(t, err, "last_error=boom")
}

func TestWaitForTaskFailedFallsBackToUnknownError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(105)
	mockModel := model.NewMockModelInterface(ctrl)

	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(&querier.AnclaxTask{
		ID:       taskID,
		Attempts: 1,
		Spec:     apigen.TaskSpec{Type: "demo-task"},
	}, nil)
	mockModel.EXPECT().GetLastTaskErrorEvent(ctx, taskID).Return(nil, pgx.ErrNoRows)

	cp := NewWorkerControlPlane(
		mockModel,
		taskgen.NewMockTaskRunner(ctrl),
		store.NewMockTaskStoreInterface(ctrl),
		terminalTaskEventListener(t, taskID, apigen.Failed),
	)

	err := cp.WaitForTask(ctx, taskID)
	require.Error(t, err)
	require.ErrorContains(t, err, "last_error=unknown")
}

func TestWaitForTaskCancelled(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(102)
	cp := NewWorkerControlPlane(
		model.NewMockModelInterface(ctrl),
		taskgen.NewMockTaskRunner(ctrl),
		store.NewMockTaskStoreInterface(ctrl),
		terminalTaskEventListener(t, taskID, apigen.Cancelled),
	)

	err := cp.WaitForTask(ctx, taskID)
	require.ErrorIs(t, err, store.ErrTaskCancelled)
}

func TestWaitForTaskNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(103)
	cp := NewWorkerControlPlane(
		model.NewMockModelInterface(ctrl),
		taskgen.NewMockTaskRunner(ctrl),
		store.NewMockTaskStoreInterface(ctrl),
		errorTaskEventListener(t, taskID, tasklistener.ErrTaskNotFound),
	)

	err := cp.WaitForTask(ctx, taskID)
	require.ErrorIs(t, err, store.ErrTaskNotFound)
}

func TestWaitForTaskListenerEventError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(104)
	waitErr := errors.New("poll failed")
	listener := &fakeTaskEventListener{
		t: t,
		wait: func(ctx context.Context, gotTaskID int32) (<-chan tasklistener.TaskTerminalEvent, error) {
			require.Equal(t, taskID, gotTaskID)
			ch := make(chan tasklistener.TaskTerminalEvent, 1)
			ch <- tasklistener.TaskTerminalEvent{TaskID: taskID, Err: waitErr}
			close(ch)
			return ch, nil
		},
	}
	cp := NewWorkerControlPlane(
		model.NewMockModelInterface(ctrl),
		taskgen.NewMockTaskRunner(ctrl),
		store.NewMockTaskStoreInterface(ctrl),
		listener,
	)

	err := cp.WaitForTask(ctx, taskID)
	require.ErrorIs(t, err, waitErr)
}

func TestWaitForTaskContextCanceled(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	taskID := int32(106)
	listener := &fakeTaskEventListener{
		t: t,
		wait: func(ctx context.Context, gotTaskID int32) (<-chan tasklistener.TaskTerminalEvent, error) {
			require.Equal(t, taskID, gotTaskID)
			return make(chan tasklistener.TaskTerminalEvent), nil
		},
	}
	cp := NewWorkerControlPlane(
		model.NewMockModelInterface(ctrl),
		taskgen.NewMockTaskRunner(ctrl),
		store.NewMockTaskStoreInterface(ctrl),
		listener,
	)

	cancel()
	err := cp.WaitForTask(ctx, taskID)
	require.ErrorContains(t, err, "context canceled")
}

func TestFormatMaxAttempts(t *testing.T) {
	require.Equal(t, "0", formatMaxAttempts(nil))
	require.Equal(t, "unlimited", formatMaxAttempts(&apigen.TaskRetryPolicy{MaxAttempts: -1}))
	require.Equal(t, "3", formatMaxAttempts(&apigen.TaskRetryPolicy{MaxAttempts: 3}))
}

func TestPauseTaskUsesTransactionAndWaitsForCancel(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(42)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	cancelTaskID := int32(99)

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &taskID).Return([]int32{13, 17}, nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, taskID).Return(nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, int32(13)).Return(nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, int32(17)).Return(nil)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{uuid.New()}, nil)
	mockRunner.EXPECT().RunBroadcastPauseTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, tx core.Tx, params *taskgen.BroadcastPauseTaskParameters, overrides ...store.TaskOverride) (int32, error) {
			require.ElementsMatch(t, []int32{taskID, 13, 17}, params.TaskIDs)
			require.Len(t, overrides, 1)
			task := &apigen.Task{Attributes: apigen.TaskAttributes{}}
			require.NoError(t, overrides[0](task))
			require.NotNil(t, task.Attributes.Priority)
			require.Equal(t, WorkerControlTaskPriority, *task.Attributes.Priority)
			return cancelTaskID, nil
		},
	)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, completedTaskEventListener(t, cancelTaskID))
	err := cp.PauseTask(ctx, taskID)
	require.NoError(t, err)
}

func TestPauseTaskWithNestedDescendants(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(50)
	childTaskID := int32(51)
	grandChildTaskID := int32(52)
	greatGrandChildTaskID := int32(53)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	cancelTaskID := int32(101)

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &taskID).Return([]int32{childTaskID, grandChildTaskID, greatGrandChildTaskID}, nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, taskID).Return(nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, childTaskID).Return(nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, grandChildTaskID).Return(nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, greatGrandChildTaskID).Return(nil)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{uuid.New()}, nil)
	mockRunner.EXPECT().RunBroadcastPauseTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, tx core.Tx, params *taskgen.BroadcastPauseTaskParameters, overrides ...store.TaskOverride) (int32, error) {
			require.ElementsMatch(t, []int32{taskID, childTaskID, grandChildTaskID, greatGrandChildTaskID}, params.TaskIDs)
			return cancelTaskID, nil
		},
	)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, completedTaskEventListener(t, cancelTaskID))
	err := cp.PauseTask(ctx, taskID)
	require.NoError(t, err)
}

func TestPauseTaskDescendantLookupFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(101)
	lookupErr := errors.New("boom")

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &taskID).Return(nil, lookupErr)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, gomock.Any()).Times(0)
	mockRunner.EXPECT().RunBroadcastPauseTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).Times(0)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, unexpectedTaskEventListener(t))
	err := cp.PauseTask(ctx, taskID)
	require.ErrorIs(t, err, lookupErr)
	require.ErrorContains(t, err, "collect task descendants")
}

func TestCancelTaskUsesTransactionAndWaitsForInterrupt(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(77)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	broadcastTaskID := int32(101)

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &taskID).Return([]int32{21}, nil)
	mockStore.EXPECT().CancelTaskWithTx(ctx, fake, taskID).Return(nil)
	mockStore.EXPECT().CancelTaskWithTx(ctx, fake, int32(21)).Return(nil)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{uuid.New()}, nil)
	mockRunner.EXPECT().RunBroadcastCancelTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, tx core.Tx, params *taskgen.BroadcastCancelTaskParameters, overrides ...store.TaskOverride) (int32, error) {
			require.ElementsMatch(t, []int32{taskID, 21}, params.TaskIDs)
			require.Len(t, overrides, 1)
			task := &apigen.Task{Attributes: apigen.TaskAttributes{}}
			require.NoError(t, overrides[0](task))
			require.NotNil(t, task.Attributes.Priority)
			require.Equal(t, WorkerControlTaskPriority, *task.Attributes.Priority)
			return broadcastTaskID, nil
		},
	)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, completedTaskEventListener(t, broadcastTaskID))
	err := cp.CancelTask(ctx, taskID)
	require.NoError(t, err)
}

func TestResumeTaskUpdatesStore(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(33)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)

	mockStore.EXPECT().ResumeTask(ctx, taskID).Return(nil)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, unexpectedTaskEventListener(t))
	err := cp.ResumeTask(ctx, taskID)
	require.NoError(t, err)
}

func TestPauseTasksByTagsUsesIntersectionAndBroadcastsDescendants(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	tags := []string{"billing", "critical"}
	exceptTagSets := [][]string{{"skip-a", "skip-b"}, {"skip-c"}}
	rootA := int32(10)
	rootB := int32(20)
	childA := int32(11)
	childB := int32(21)
	duplicateChild := childA

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	broadcastTaskID := int32(701)
	workerID := uuid.New()

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskIDsByTags(ctx, taskTagsQueryParams(tags, exceptTagSets...)).Return([]int32{rootA, rootB}, nil)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &rootA).Return([]int32{childA}, nil)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &rootB).Return([]int32{duplicateChild, childB}, nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, rootA).Return(nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, childA).Return(nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, rootB).Return(nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, childB).Return(nil)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{workerID}, nil)
	mockRunner.EXPECT().RunBroadcastPauseTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, tx core.Tx, params *taskgen.BroadcastPauseTaskParameters, overrides ...store.TaskOverride) (int32, error) {
			require.ElementsMatch(t, []int32{rootA, childA, rootB, childB}, params.TaskIDs)
			require.Equal(t, []uuid.UUID{workerID}, params.WorkerIDs)
			require.Len(t, overrides, 1)
			task := &apigen.Task{Attributes: apigen.TaskAttributes{}}
			require.NoError(t, overrides[0](task))
			require.NotNil(t, task.Attributes.Priority)
			require.Equal(t, WorkerControlTaskPriority, *task.Attributes.Priority)
			return broadcastTaskID, nil
		},
	)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, completedTaskEventListener(t, broadcastTaskID))
	err := cp.PauseTasksByTags(ctx, tags, exceptTagSets...)
	require.NoError(t, err)
}

func TestCancelTasksByTagsUsesIntersectionAndBroadcasts(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	tags := []string{"tenant:acme", "gpu"}
	taskID := int32(30)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	broadcastTaskID := int32(702)
	workerID := uuid.New()

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskIDsByTags(ctx, taskTagsQueryParams(tags)).Return([]int32{taskID}, nil)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &taskID).Return(nil, nil)
	mockStore.EXPECT().CancelTaskWithTx(ctx, fake, taskID).Return(nil)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{workerID}, nil)
	mockRunner.EXPECT().RunBroadcastCancelTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, tx core.Tx, params *taskgen.BroadcastCancelTaskParameters, overrides ...store.TaskOverride) (int32, error) {
			require.Equal(t, []int32{taskID}, params.TaskIDs)
			require.Equal(t, []uuid.UUID{workerID}, params.WorkerIDs)
			require.Len(t, overrides, 1)
			task := &apigen.Task{Attributes: apigen.TaskAttributes{}}
			require.NoError(t, overrides[0](task))
			require.NotNil(t, task.Attributes.Priority)
			require.Equal(t, WorkerControlTaskPriority, *task.Attributes.Priority)
			return broadcastTaskID, nil
		},
	)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, completedTaskEventListener(t, broadcastTaskID))
	err := cp.CancelTasksByTags(ctx, tags)
	require.NoError(t, err)
}

func TestPauseTasksByTagsWithTxDedupesTagsAndReturnsBroadcastTaskID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	tags := []string{"billing"}
	taskID := int32(40)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	broadcastTaskID := int32(703)

	mockModel.EXPECT().SpawnWithTx(fake).Return(mockModel)
	mockModel.EXPECT().ListTaskIDsByTags(ctx, taskTagsQueryParams(tags)).Return([]int32{taskID}, nil)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &taskID).Return(nil, nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, taskID).Return(nil)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{uuid.New()}, nil)
	mockRunner.EXPECT().RunBroadcastPauseTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).Return(broadcastTaskID, nil)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, unexpectedTaskEventListener(t))
	got, err := cp.PauseTasksByTagsWithTx(ctx, fake, []string{"billing", "billing"})
	require.NoError(t, err)
	require.Equal(t, broadcastTaskID, got)
}

func TestResumeTasksByTagsUsesTransactionAndDedupesTasks(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	tags := []string{"billing", "critical"}
	exceptTagSets := [][]string{{"paused-except"}}
	taskA := int32(50)
	taskB := int32(51)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskIDsByTags(ctx, taskTagsQueryParams(tags, exceptTagSets...)).Return([]int32{taskA, taskB, taskA}, nil)
	mockStore.EXPECT().ResumeTaskWithTx(ctx, fake, taskA).Return(nil)
	mockStore.EXPECT().ResumeTaskWithTx(ctx, fake, taskB).Return(nil)
	mockRunner.EXPECT().RunBroadcastPauseTaskWithTx(ctx, gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	mockRunner.EXPECT().RunBroadcastCancelTaskWithTx(ctx, gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, unexpectedTaskEventListener(t))
	err := cp.ResumeTasksByTags(ctx, tags, exceptTagSets...)
	require.NoError(t, err)
}

func TestPauseTasksByTagsNoMatchesSkipsBroadcast(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	tags := []string{"missing"}

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskIDsByTags(ctx, taskTagsQueryParams(tags)).Return(nil, nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, gomock.Any()).Times(0)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Times(0)
	mockRunner.EXPECT().RunBroadcastPauseTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).Times(0)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, unexpectedTaskEventListener(t))
	err := cp.PauseTasksByTags(ctx, tags)
	require.NoError(t, err)
}

func TestPauseTaskByUniqueTagResolvesTaskID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	uniqueTag := "pause-tag"
	taskID := int32(55)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	cancelTaskID := int32(88)

	mockStore.EXPECT().GetTaskByUniqueTag(ctx, uniqueTag).Return(&apigen.Task{ID: taskID}, nil)
	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &taskID).Return([]int32{}, nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, taskID).Return(nil)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{uuid.New()}, nil)
	mockRunner.EXPECT().RunBroadcastPauseTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, tx core.Tx, params *taskgen.BroadcastPauseTaskParameters, overrides ...store.TaskOverride) (int32, error) {
			require.Equal(t, []int32{taskID}, params.TaskIDs)
			return cancelTaskID, nil
		},
	)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, completedTaskEventListener(t, cancelTaskID))
	err := cp.PauseTaskByUniqueTag(ctx, uniqueTag)
	require.NoError(t, err)
}

func TestCancelTaskByUniqueTagResolvesTaskID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	uniqueTag := "cancel-tag"
	taskID := int32(66)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	broadcastTaskID := int32(101)

	mockStore.EXPECT().GetTaskByUniqueTag(ctx, uniqueTag).Return(&apigen.Task{ID: taskID}, nil)
	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &taskID).Return([]int32{}, nil)
	mockStore.EXPECT().CancelTaskWithTx(ctx, fake, taskID).Return(nil)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{uuid.New()}, nil)
	mockRunner.EXPECT().RunBroadcastCancelTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, tx core.Tx, params *taskgen.BroadcastCancelTaskParameters, overrides ...store.TaskOverride) (int32, error) {
			require.Equal(t, []int32{taskID}, params.TaskIDs)
			return broadcastTaskID, nil
		},
	)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, completedTaskEventListener(t, broadcastTaskID))
	err := cp.CancelTaskByUniqueTag(ctx, uniqueTag)
	require.NoError(t, err)
}

func TestUpdateWorkerRuntimeConfigNilRequest(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cp := NewWorkerControlPlane(model.NewMockModelInterface(ctrl), taskgen.NewMockTaskRunner(ctrl), store.NewMockTaskStoreInterface(ctrl), unexpectedTaskEventListener(t))
	err := cp.UpdateWorkerRuntimeConfig(context.Background(), nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot be nil")
}

func TestUpdateWorkerRuntimeConfigRunTaskError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, unexpectedTaskEventListener(t))

	errBoom := errors.New("boom")
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{}, nil)
	mockRunner.EXPECT().RunBroadcastUpdateWorkerRuntimeConfig(ctx, gomock.Any(), gomock.Any()).Return(int32(0), errBoom)

	err := cp.UpdateWorkerRuntimeConfig(ctx, &UpdateWorkerRuntimeConfigRequest{})
	require.Error(t, err)
	require.ErrorContains(t, err, "run update worker runtime config task")
}

func TestUpdateWorkerRuntimeConfigWaitError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)

	errBoom := errors.New("wait failed")
	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, errorTaskEventListener(t, 123, errBoom))
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{}, nil)
	mockRunner.EXPECT().RunBroadcastUpdateWorkerRuntimeConfig(ctx, gomock.Any(), gomock.Any()).Return(int32(123), nil)

	err := cp.UpdateWorkerRuntimeConfig(ctx, &UpdateWorkerRuntimeConfigRequest{})
	require.Error(t, err)
	require.ErrorContains(t, err, "wait for update worker runtime config task")
}

func TestPauseCancelResumeInvalidInput(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cp := NewWorkerControlPlane(model.NewMockModelInterface(ctrl), taskgen.NewMockTaskRunner(ctrl), store.NewMockTaskStoreInterface(ctrl), unexpectedTaskEventListener(t))

	err := cp.PauseTask(context.Background(), 0)
	require.ErrorContains(t, err, "positive taskID")
	err = cp.CancelTask(context.Background(), 0)
	require.ErrorContains(t, err, "positive taskID")
	err = cp.ResumeTask(context.Background(), 0)
	require.ErrorContains(t, err, "positive taskID")
	err = cp.PauseTasksByTags(context.Background(), nil)
	require.ErrorContains(t, err, "at least one tag")
	err = cp.CancelTasksByTags(context.Background(), []string{""})
	require.ErrorContains(t, err, "non-empty tags")
	err = cp.ResumeTasksByTags(context.Background(), []string{})
	require.ErrorContains(t, err, "at least one tag")
	err = cp.PauseTasksByTags(context.Background(), []string{"billing"}, []string{})
	require.ErrorContains(t, err, "non-empty except tag set")
	err = cp.CancelTasksByTags(context.Background(), []string{"billing"}, []string{"ok", ""})
	require.ErrorContains(t, err, "non-empty except tag set")
}

func TestPauseTaskWaitError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(42)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	broadcastTaskID := int32(321)

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &taskID).Return([]int32{}, nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, taskID).Return(nil)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{uuid.New()}, nil)
	mockRunner.EXPECT().RunBroadcastPauseTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).Return(broadcastTaskID, nil)

	waitErr := errors.New("wait boom")
	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, errorTaskEventListener(t, broadcastTaskID, waitErr))
	err := cp.PauseTask(ctx, taskID)
	require.Error(t, err)
	require.ErrorContains(t, err, "wait for broadcast pause task")
}

func TestCancelTaskWaitError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(42)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	broadcastTaskID := int32(654)

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &taskID).Return([]int32{}, nil)
	mockStore.EXPECT().CancelTaskWithTx(ctx, fake, taskID).Return(nil)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{uuid.New()}, nil)
	mockRunner.EXPECT().RunBroadcastCancelTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).Return(broadcastTaskID, nil)

	waitErr := errors.New("wait boom")
	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, errorTaskEventListener(t, broadcastTaskID, waitErr))
	err := cp.CancelTask(ctx, taskID)
	require.Error(t, err)
	require.ErrorContains(t, err, "wait for broadcast cancel task")
}

func TestPauseTaskSkipsBroadcastWhenNoAliveWorkers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(42)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &taskID).Return([]int32{}, nil)
	mockStore.EXPECT().PauseTaskWithTx(ctx, fake, taskID).Return(nil)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return(nil, nil)
	mockRunner.EXPECT().RunBroadcastPauseTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).Times(0)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, unexpectedTaskEventListener(t))
	err := cp.PauseTask(ctx, taskID)
	require.NoError(t, err)
}

func TestCancelTaskSkipsBroadcastWhenNoAliveWorkers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(42)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockModel.EXPECT().ListTaskDescendantIDs(ctx, &taskID).Return([]int32{}, nil)
	mockStore.EXPECT().CancelTaskWithTx(ctx, fake, taskID).Return(nil)
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return(nil, nil)
	mockRunner.EXPECT().RunBroadcastCancelTaskWithTx(ctx, fake, gomock.Any(), gomock.Any()).Times(0)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, unexpectedTaskEventListener(t))
	err := cp.CancelTask(ctx, taskID)
	require.NoError(t, err)
}

func TestResumeTaskStoreError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, unexpectedTaskEventListener(t))

	mockStore.EXPECT().ResumeTask(ctx, int32(10)).Return(errors.New("boom"))
	err := cp.ResumeTask(ctx, 10)
	require.Error(t, err)
	require.ErrorContains(t, err, "resume task")
}

func TestUniqueTagControlPlaneValidationAndLookupErrors(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, unexpectedTaskEventListener(t))

	err := cp.PauseTaskByUniqueTag(ctx, "")
	require.ErrorContains(t, err, "requires unique tag")
	err = cp.CancelTaskByUniqueTag(ctx, "")
	require.ErrorContains(t, err, "requires unique tag")
	err = cp.ResumeTaskByUniqueTag(ctx, "")
	require.ErrorContains(t, err, "requires unique tag")

	mockStore.EXPECT().GetTaskByUniqueTag(ctx, "x").Return(nil, errors.New("lookup"))
	err = cp.PauseTaskByUniqueTag(ctx, "x")
	require.ErrorContains(t, err, "get task by unique tag")
}

func TestResumeTaskByUniqueTagResolvesTaskID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	uniqueTag := "resume-tag"
	taskID := int32(91)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)

	mockStore.EXPECT().GetTaskByUniqueTag(ctx, uniqueTag).Return(&apigen.Task{ID: taskID}, nil)
	mockStore.EXPECT().ResumeTask(ctx, taskID).Return(nil)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore, unexpectedTaskEventListener(t))
	err := cp.ResumeTaskByUniqueTag(ctx, uniqueTag)
	require.NoError(t, err)
}
