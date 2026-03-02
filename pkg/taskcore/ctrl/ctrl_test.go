package ctrl

import (
	"context"
	"testing"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
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
	mockStoreTx := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	cancelTaskID := int32(99)

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockStore.EXPECT().WithTx(fake).Return(mockStoreTx)
	mockStoreTx.EXPECT().PauseTask(ctx, taskID).Return(nil)
	mockRunner.EXPECT().RunInterruptTaskWithTx(ctx, fake, gomock.Any()).DoAndReturn(
		func(ctx context.Context, tx core.Tx, params *taskgen.InterruptTaskParameters, overrides ...store.TaskOverride) (int32, error) {
			require.Equal(t, taskID, params.TaskID)
			return cancelTaskID, nil
		},
	)
	mockStore.EXPECT().WaitForTask(ctx, cancelTaskID).Return(nil)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
	err := cp.PauseTask(ctx, taskID)
	require.NoError(t, err)
}

func TestCancelTaskUsesTransactionAndWaitsForInterrupt(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(77)

	mockModel := model.NewMockModelInterface(ctrl)
	mockStore := store.NewMockTaskStoreInterface(ctrl)
	mockStoreTx := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	interruptTaskID := int32(101)

	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockStore.EXPECT().WithTx(fake).Return(mockStoreTx)
	mockStoreTx.EXPECT().CancelTask(ctx, taskID).Return(nil)
	mockRunner.EXPECT().RunInterruptTaskWithTx(ctx, fake, gomock.Any()).DoAndReturn(
		func(ctx context.Context, tx core.Tx, params *taskgen.InterruptTaskParameters, overrides ...store.TaskOverride) (int32, error) {
			require.Equal(t, taskID, params.TaskID)
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
	mockStoreTx := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	cancelTaskID := int32(88)

	mockStore.EXPECT().GetTaskByUniqueTag(ctx, uniqueTag).Return(&apigen.Task{ID: taskID}, nil)
	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockStore.EXPECT().WithTx(fake).Return(mockStoreTx)
	mockStoreTx.EXPECT().PauseTask(ctx, taskID).Return(nil)
	mockRunner.EXPECT().RunInterruptTaskWithTx(ctx, fake, gomock.Any()).DoAndReturn(
		func(ctx context.Context, tx core.Tx, params *taskgen.InterruptTaskParameters, overrides ...store.TaskOverride) (int32, error) {
			require.Equal(t, taskID, params.TaskID)
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
	mockStoreTx := store.NewMockTaskStoreInterface(ctrl)
	mockRunner := taskgen.NewMockTaskRunner(ctrl)
	fake := &fakeTx{}
	interruptTaskID := int32(101)

	mockStore.EXPECT().GetTaskByUniqueTag(ctx, uniqueTag).Return(&apigen.Task{ID: taskID}, nil)
	mockModel.EXPECT().RunTransactionWithTx(ctx, gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(core.Tx, model.ModelInterface) error) error {
			return fn(fake, mockModel)
		},
	)
	mockStore.EXPECT().WithTx(fake).Return(mockStoreTx)
	mockStoreTx.EXPECT().CancelTask(ctx, taskID).Return(nil)
	mockRunner.EXPECT().RunInterruptTaskWithTx(ctx, fake, gomock.Any()).DoAndReturn(
		func(ctx context.Context, tx core.Tx, params *taskgen.InterruptTaskParameters, overrides ...store.TaskOverride) (int32, error) {
			require.Equal(t, taskID, params.TaskID)
			return interruptTaskID, nil
		},
	)
	mockStore.EXPECT().WaitForTask(ctx, interruptTaskID).Return(nil)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
	err := cp.CancelTaskByUniqueTag(ctx, uniqueTag)
	require.NoError(t, err)
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
