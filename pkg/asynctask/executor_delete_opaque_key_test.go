package asynctask

import (
	"context"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestExecuteDeleteOpaqueKeyCallsModel(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().DeleteOpaqueKey(ctx, int64(1)).Return(nil)

	exec := &Executor{model: mockModel, now: time.Now}
	err := exec.ExecuteDeleteOpaqueKey(ctx, &taskgen.DeleteOpaqueKeyParameters{KeyID: 1})
	require.NoError(t, err)
}

func TestOnDeleteOpaqueKeyFailedNoop(t *testing.T) {
	exec := &Executor{}
	err := exec.OnDeleteOpaqueKeyFailed(context.Background(), 1, &taskgen.DeleteOpaqueKeyParameters{}, nil)
	require.NoError(t, err)
}
