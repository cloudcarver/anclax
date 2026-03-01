package ctrl

import (
	"context"
	"testing"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
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
	params := &taskgen.PauseTaskParameters{TaskID: 42}

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
	mockStoreTx.EXPECT().PauseTask(ctx, params.TaskID).Return(nil)
	mockRunner.EXPECT().RunPauseTaskWithTx(ctx, fake, params).Return(cancelTaskID, nil)
	mockStore.EXPECT().WaitForTask(ctx, cancelTaskID).Return(nil)

	cp := NewWorkerControlPlane(mockModel, mockRunner, mockStore)
	err := cp.PauseTask(ctx, params)
	require.NoError(t, err)
}
