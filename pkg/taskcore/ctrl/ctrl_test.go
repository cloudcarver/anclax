package ctrl

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
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
	mockStore.EXPECT().WaitForTask(ctx, cancelTaskID).Return(nil)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
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
	mockStore.EXPECT().WaitForTask(ctx, cancelTaskID).Return(nil)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
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
	mockStore.EXPECT().WaitForTask(ctx, gomock.Any()).Times(0)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
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
	interruptTaskID := int32(101)

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
			return interruptTaskID, nil
		},
	)
	mockStore.EXPECT().WaitForTask(ctx, interruptTaskID).Return(nil)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
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

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
	err := cp.ResumeTask(ctx, taskID)
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
	mockStore.EXPECT().WaitForTask(ctx, cancelTaskID).Return(nil)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
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
	interruptTaskID := int32(101)

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
			return interruptTaskID, nil
		},
	)
	mockStore.EXPECT().WaitForTask(ctx, interruptTaskID).Return(nil)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
	err := cp.CancelTaskByUniqueTag(ctx, uniqueTag)
	require.NoError(t, err)
}

func TestUpdateWorkerRuntimeConfigNilRequest(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cp := NewWorkerControlPlane(model.NewMockModelInterface(ctrl), taskgen.NewMockTaskRunner(ctrl), store.NewMockTaskStoreInterface(ctrl))
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
	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)

	errBoom := errors.New("boom")
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{}, nil)
	mockRunner.EXPECT().RunBroadcastUpdateWorkerRuntimeConfig(ctx, gomock.Any(), gomock.Any()).Return(int32(0), errBoom)
	mockStore.EXPECT().WaitForTask(ctx, gomock.Any()).Times(0)

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
	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)

	errBoom := errors.New("wait failed")
	mockModel.EXPECT().ListOnlineWorkerIDs(ctx, gomock.Any()).Return([]uuid.UUID{}, nil)
	mockRunner.EXPECT().RunBroadcastUpdateWorkerRuntimeConfig(ctx, gomock.Any(), gomock.Any()).Return(int32(123), nil)
	mockStore.EXPECT().WaitForTask(ctx, int32(123)).Return(errBoom)

	err := cp.UpdateWorkerRuntimeConfig(ctx, &UpdateWorkerRuntimeConfigRequest{})
	require.Error(t, err)
	require.ErrorContains(t, err, "wait for update worker runtime config task")
}

func TestPauseCancelResumeInvalidInput(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cp := NewWorkerControlPlane(model.NewMockModelInterface(ctrl), taskgen.NewMockTaskRunner(ctrl), store.NewMockTaskStoreInterface(ctrl))

	err := cp.PauseTask(context.Background(), 0)
	require.ErrorContains(t, err, "positive taskID")
	err = cp.CancelTask(context.Background(), 0)
	require.ErrorContains(t, err, "positive taskID")
	err = cp.ResumeTask(context.Background(), 0)
	require.ErrorContains(t, err, "positive taskID")
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
	mockStore.EXPECT().WaitForTask(ctx, broadcastTaskID).Return(errors.New("wait boom"))

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
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
	mockStore.EXPECT().WaitForTask(ctx, broadcastTaskID).Return(errors.New("wait boom"))

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
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
	mockStore.EXPECT().WaitForTask(ctx, gomock.Any()).Times(0)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
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
	mockStore.EXPECT().WaitForTask(ctx, gomock.Any()).Times(0)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
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
	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)

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
	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)

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

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
	err := cp.ResumeTaskByUniqueTag(ctx, uniqueTag)
	require.NoError(t, err)
}
