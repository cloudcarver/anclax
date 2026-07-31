package store

import (
	"context"
	"testing"
	"time"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestCreate(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)
	taskRunner := taskgen.NewMockTaskRunner(ctrl)

	var (
		ctx      = context.Background()
		ttl      = 1 * time.Hour
		key      = []byte("test")
		group    = "user:201"
		currTime = time.Now()
		keyID    = int64(101)
		taskID   = int32(101)
	)

	mockModel.EXPECT().CreateOpaqueKey(gomock.Any(), querier.CreateOpaqueKeyParams{
		Group:           &group,
		Key:             key,
		TtlMicroseconds: ttl.Microseconds(),
	}).Return(&querier.CreateOpaqueKeyRow{
		ID:        keyID,
		ExpiresAt: currTime.Add(ttl),
	}, nil)
	taskRunner.EXPECT().RunDeleteOpaqueKeyWithTx(
		ctx,
		gomock.Any(),
		&taskgen.DeleteOpaqueKeyParameters{
			KeyID: keyID,
		},
		taskcore.Eq(taskcore.WithStartedAt(currTime.Add(ttl))),
	).Return(taskID, nil)

	store := &Store{
		model:      mockModel,
		taskRunner: taskRunner,
	}

	ret, err := store.Create(ctx, key, ttl, group)
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
			name: "missing or expired key",
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

func TestConsumeRequiresMatchingLiveKey(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	keyID := int64(101)
	key := []byte("secret-key")

	tests := []struct {
		name     string
		storeErr error
		wantErr  error
	}{
		{name: "success"},
		{name: "missing expired or mismatched key", storeErr: pgx.ErrNoRows, wantErr: ErrKeyNotFound},
		{name: "database error", storeErr: errors.New("database error")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)
			mockModel.EXPECT().ConsumeOpaqueKey(ctx, querier.ConsumeOpaqueKeyParams{
				ID:  keyID,
				Key: key,
			}).Return(keyID, tt.storeErr)

			keyStore := &Store{model: mockModel}
			err := keyStore.Consume(ctx, keyID, key)
			if tt.storeErr == nil {
				require.NoError(t, err)
			} else if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.ErrorIs(t, err, tt.storeErr)
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

func TestDeleteGroupKeysDeletesGroupKeys(t *testing.T) {
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
		group = "user:201"
	)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			model := model.NewMockModelInterfaceWithTransaction(ctrl)

			store := &Store{
				model: model,
			}

			if tc.err == nil {
				model.EXPECT().DeleteOpaqueKeys(gomock.Any(), &group).Return(nil)
			} else {
				model.EXPECT().DeleteOpaqueKeys(gomock.Any(), &group).Return(tc.err)
			}

			err := store.DeleteGroupKeys(ctx, group)
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
