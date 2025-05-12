package store

import (
	"context"
	"testing"
	"time"

	"github.com/cloudcarver/anchor/pkg/model"
	"github.com/cloudcarver/anchor/pkg/taskcore/runner"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestCreate(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)
	taskRunner := runner.NewMockTaskRunner(ctrl)

	var (
		ctx      = context.Background()
		ttl      = 1 * time.Hour
		key      = []byte("test")
		userID   = int32(201)
		currTime = time.Now()
		keyID    = int64(101)
		taskID   = int32(101)
	)

	mockModel.EXPECT().CreateOpaqueKey(gomock.Any(), gomock.Any()).Return(keyID, nil)
	taskRunner.EXPECT().RunDeleteOpaqueKey(
		ctx,
		&runner.DeleteOpaqueKeyParameters{
			KeyID: keyID,
		},
	).Return(taskID, nil)

	store := &Store{
		model:      mockModel,
		taskRunner: taskRunner,
		now:        func() time.Time { return currTime },
	}

	ret, err := store.Create(ctx, userID, key, ttl)
	require.NoError(t, err)
	require.Equal(t, keyID, ret)
}

func TestDelete(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		ctx   = context.Background()
		keyID = int64(101)
	)

	var testCases = []struct {
		name string
		err  error
	}{
		{
			name: "success",
			err:  nil,
		},
		{
			name: "no row",
			err:  pgx.ErrNoRows,
		},
		{
			name: "error",
			err:  errors.New("error"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			model := model.NewMockModelInterfaceWithTransaction(ctrl)

			store := &Store{
				model: model,
			}

			if tc.err == nil {
				model.EXPECT().DeleteOpaqueKey(gomock.Any(), keyID).Return(nil)
			} else {
				model.EXPECT().DeleteOpaqueKey(gomock.Any(), keyID).Return(tc.err)
			}

			err := store.Delete(ctx, keyID)
			if tc.err == nil {
				require.NoError(t, err)
			} else if tc.err == pgx.ErrNoRows {
				require.ErrorIs(t, err, ErrKeyNotFound)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestGet(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var testCases = []struct {
		name string
		err  error
	}{
		{
			name: "success",
			err:  nil,
		},
		{
			name: "no row",
			err:  pgx.ErrNoRows,
		},
		{
			name: "error",
			err:  errors.New("error"),
		},
	}

	var (
		ctx   = context.Background()
		keyID = int64(101)
		key   = []byte("test")
	)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			model := model.NewMockModelInterfaceWithTransaction(ctrl)

			store := &Store{
				model: model,
			}

			if tc.err == nil {
				model.EXPECT().GetOpaqueKey(gomock.Any(), keyID).Return(key, nil)
			} else {
				model.EXPECT().GetOpaqueKey(gomock.Any(), keyID).Return(nil, tc.err)
			}

			key, err := store.Get(ctx, keyID)
			if tc.err == nil {
				require.NoError(t, err)
				require.Equal(t, key, key)
			} else if tc.err == pgx.ErrNoRows {
				require.ErrorIs(t, err, ErrKeyNotFound)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestDeleteUserKeys(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var testCases = []struct {
		name string
		err  error
	}{
		{
			name: "success",
			err:  nil,
		},
		{
			name: "no row",
			err:  pgx.ErrNoRows,
		},
		{
			name: "error",
			err:  errors.New("error"),
		},
	}

	var (
		ctx    = context.Background()
		userID = int32(201)
	)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			model := model.NewMockModelInterfaceWithTransaction(ctrl)

			store := &Store{
				model: model,
			}

			if tc.err == nil {
				model.EXPECT().DeleteOpaqueKeys(gomock.Any(), userID).Return(nil)
			} else {
				model.EXPECT().DeleteOpaqueKeys(gomock.Any(), userID).Return(tc.err)
			}

			err := store.DeleteUserKeys(ctx, userID)
			if tc.err == nil {
				require.NoError(t, err)
			} else if tc.err == pgx.ErrNoRows {
				require.ErrorIs(t, err, ErrKeyNotFound)
			} else {
				require.Error(t, err)
			}
		})
	}
}
