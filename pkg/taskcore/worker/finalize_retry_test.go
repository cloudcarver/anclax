package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestFinalizeRetriesOnlyRolledBackTransactions(t *testing.T) {
	for _, code := range []string{"40P01", "40001", "55P03", "23514", "08006"} {
		t.Run(code, func(t *testing.T) {
			m := model.NewMockModelInterface(gomock.NewController(t))
			p, err := NewModelPort(m, uuid.New(), nil, nil, time.Second, 0)
			require.NoError(t, err)
			failure := &pgconn.PgError{Code: code}
			calls := 1
			retry := code == "40P01" || code == "40001" || code == "55P03"
			if retry {
				calls = 2
			}
			n := 0
			m.EXPECT().RunTransactionWithTx(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ func(core.Tx, model.ModelInterface) error) error {
				_, bounded := ctx.Deadline()
				require.True(t, bounded)
				n++
				if n == 1 {
					return failure
				}
				return nil
			}).Times(calls)
			err = p.FinalizeTask(context.Background(), Task{ID: 1, LeaseVersion: 2}, nil)
			if retry {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, failure)
			}
		})
	}
}

func TestFinalizeRetryHonorsDeadlineAndReleasesRuntime(t *testing.T) {
	m := model.NewMockModelInterface(gomock.NewController(t))
	p, err := NewModelPort(m, uuid.New(), nil, nil, time.Second, 0)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	m.EXPECT().RunTransactionWithTx(gomock.Any(), gomock.Any()).Return(&pgconn.PgError{Code: "40P01"}).MinTimes(1)
	p.registerTaskRuntime(Task{ID: 1}, func(error) {})
	err = p.FinalizeTask(ctx, Task{ID: 1}, nil)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
	require.False(t, p.TaskRuntimesActive([]int32{1}))
}
