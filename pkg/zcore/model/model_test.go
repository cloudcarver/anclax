package model

import (
	context "context"
	"testing"

	"github.com/cloudcarver/anclax/core"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestModelClose_ctx_cancel_hang_tx(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockTx := core.NewMockTx(ctrl)

	m := &Model{
		beginTx: func(ctx context.Context) (core.Tx, error) {
			return mockTx, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	mockTx.EXPECT().Rollback(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Fail()
		}
		return nil
	})

	err := m.RunTransactionWithTx(ctx, func(tx core.Tx, model ModelInterface) error {
		return ctx.Err()
	})

	require.Error(t, err)
}
