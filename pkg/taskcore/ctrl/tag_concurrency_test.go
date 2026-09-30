package ctrl

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestTagConcurrencyValidationAndErrors(t *testing.T) {
	ctx := context.Background()
	m := model.NewMockModelInterface(gomock.NewController(t))
	s := NewWorkerControlPlane(m, nil, nil, nil)
	require.Error(t, s.SetTagConcurrencyLimit(ctx, "", 1))
	require.Error(t, s.SetTagConcurrencyLimit(ctx, "tenant:1", -1))
	require.Error(t, s.RemoveTagConcurrencyLimit(ctx, ""))
	_, err := s.GetTagConcurrency(ctx, "")
	require.Error(t, err)
	for _, size := range []int32{-1, 0, 1001} {
		_, err := s.ListTagConcurrencyLimits(ctx, "", size)
		require.Error(t, err)
	}

	m.EXPECT().SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: "tenant:1", MaxConcurrency: 0}).Return(nil)
	require.NoError(t, s.SetTagConcurrencyLimit(ctx, "tenant:1", 0))
	dbErr := errors.New("database unavailable")
	m.EXPECT().RemoveTaskTagConcurrencyLimit(ctx, "tenant:1").Return(dbErr)
	require.ErrorIs(t, s.RemoveTagConcurrencyLimit(ctx, "tenant:1"), dbErr)
	m.EXPECT().GetTaskTagConcurrency(ctx, "unknown").Return(nil, pgx.ErrNoRows)
	state, err := s.GetTagConcurrency(ctx, "unknown")
	require.NoError(t, err)
	require.Equal(t, &TagConcurrency{Tag: "unknown"}, state)
	m.EXPECT().GetTaskTagConcurrency(ctx, "known").Return(nil, dbErr)
	_, err = s.GetTagConcurrency(ctx, "known")
	require.ErrorIs(t, err, dbErr)

	limit := int32(2)
	m.EXPECT().ListTaskTagConcurrencyLimits(ctx, querier.ListTaskTagConcurrencyLimitsParams{AfterTag: "tenant:1", PageSize: 2}).Return([]*querier.AnclaxTaskTagConcurrency{
		{Tag: "tenant:2", MaxConcurrency: &limit, InUse: 3},
	}, nil)
	rows, err := s.ListTagConcurrencyLimits(ctx, "tenant:1", 2)
	require.NoError(t, err)
	require.Equal(t, []TagConcurrency{{Tag: "tenant:2", MaxConcurrency: &limit, InUse: 3}}, rows)
}
