package model

import (
	context "context"
	"strings"
	"testing"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewModelDoesNotReturnDSNCredentials(t *testing.T) {
	dsn := "postgres://audit-user:database-secret-canary@%zz/database"

	_, err := NewModel(&config.Config{
		Pg: config.Pg{DSN: &dsn},
	}, config.DefaultLibConfig(), closer.NewCloserManager())

	require.Error(t, err)
	require.NotContains(t, err.Error(), "database-secret-canary")
	require.NotContains(t, err.Error(), dsn)
	require.Equal(t, "failed to parse database configuration", strings.TrimSpace(err.Error()))
}

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
