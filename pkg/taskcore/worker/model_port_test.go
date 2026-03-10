package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/taskcore/pgnotify"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestModelPortClaimByIDNoTask(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	mockTxModel := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockTx := core.NewMockTx(ctrl)

	port, err := NewModelPort(mockModel, workerID, []string{"ops"}, nil, 5*time.Second, 0)
	require.NoError(t, err)

	mockModel.EXPECT().RunTransactionWithTx(context.Background(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
			return f(mockTx, mockTxModel)
		},
	).Times(1)
	mockTxModel.EXPECT().ClaimTaskByID(context.Background(), gomock.AssignableToTypeOf(querier.ClaimTaskByIDParams{})).
		Return(nil, pgx.ErrNoRows).Times(1)

	task, err := port.ClaimByID(context.Background(), 42, ClaimRequest{})
	require.Nil(t, task)
	require.ErrorIs(t, err, ErrNoTask)
}

func TestModelPortRefreshRuntimeConfigDecode(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, nil, nil, 5*time.Second, 0)
	require.NoError(t, err)

	mockModel.EXPECT().GetLatestWorkerRuntimeConfig(context.Background()).Return(&querier.AnclaxWorkerRuntimeConfig{
		Version: 7,
		Payload: []byte(`{"maxStrictPercentage":30,"labelWeights":{"default":1,"w1":3}}`),
	}, nil)

	cfg, err := port.RefreshRuntimeConfig(context.Background(), workerID.String(), "")
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, int64(7), cfg.Version)
	require.NotNil(t, cfg.MaxStrictPercentage)
	require.Equal(t, int32(30), *cfg.MaxStrictPercentage)
	require.Equal(t, int32(3), cfg.LabelWeights["w1"])
}

func TestHasUserClaimLabels(t *testing.T) {
	t.Run("only reserved worker labels", func(t *testing.T) {
		require.False(t, hasUserClaimLabels([]string{"worker:abc", "worker:def"}))
	})
	t.Run("contains non-reserved label", func(t *testing.T) {
		require.True(t, hasUserClaimLabels([]string{"worker:abc", "ops"}))
	})
	t.Run("empty labels", func(t *testing.T) {
		require.False(t, hasUserClaimLabels(nil))
	})
}

func TestNewModelPortHasLabelsIgnoresReservedWorkerLabel(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)

	port, err := NewModelPort(mockModel, workerID, []string{"worker:" + workerID.String()}, nil, 5*time.Second, 0)
	require.NoError(t, err)
	require.False(t, port.hasLabels)

	port2, err := NewModelPort(mockModel, workerID, []string{"worker:" + workerID.String(), "ops"}, nil, 5*time.Second, 0)
	require.NoError(t, err)
	require.True(t, port2.hasLabels)
}

func TestModelPortTaskInterruptCauseFromStore(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, nil, nil, 5*time.Second, 0)
	require.NoError(t, err)

	taskID := int32(9)

	mockModel.EXPECT().GetTaskByID(context.Background(), taskID).Return(&querier.AnclaxTask{Status: string(apigen.Paused)}, nil)
	require.ErrorIs(t, port.taskInterruptCauseFromStore(context.Background(), taskID), taskcore.ErrTaskPaused)

	mockModel.EXPECT().GetTaskByID(context.Background(), taskID).Return(&querier.AnclaxTask{Status: string(apigen.Cancelled)}, nil)
	require.ErrorIs(t, port.taskInterruptCauseFromStore(context.Background(), taskID), taskcore.ErrTaskCancelled)

	mockModel.EXPECT().GetTaskByID(context.Background(), taskID).Return(nil, pgx.ErrNoRows)
	require.ErrorIs(t, port.taskInterruptCauseFromStore(context.Background(), taskID), taskcore.ErrTaskInterrupted)
}

func TestModelPortAckRuntimeConfigApplied(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	mockModel := model.NewMockModelInterface(ctrl)
	port, err := NewModelPort(mockModel, workerID, nil, nil, 5*time.Second, 0)
	require.NoError(t, err)

	const requestID = "req-77"
	const appliedVersion int64 = 11
	mockModel.EXPECT().UpdateWorkerAppliedConfigVersion(context.Background(), querier.UpdateWorkerAppliedConfigVersionParams{
		ID:                   workerID,
		AppliedConfigVersion: appliedVersion,
	}).Return(nil)
	mockModel.EXPECT().NotifyWorkerRuntimeConfigAck(context.Background(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, payload string) error {
			var ack pgnotify.RuntimeConfigAckNotification
			require.NoError(t, json.Unmarshal([]byte(payload), &ack))
			require.Equal(t, pgnotify.OpAck, ack.Op)
			require.Equal(t, requestID, ack.Params.RequestID)
			require.Equal(t, workerID.String(), ack.Params.WorkerID)
			require.Equal(t, appliedVersion, ack.Params.AppliedVersion)
			return nil
		},
	)

	err = port.AckRuntimeConfigApplied(context.Background(), workerID.String(), requestID, appliedVersion)
	require.NoError(t, err)
}
