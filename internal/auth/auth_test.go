package auth

import (
	"context"
	"testing"

	"github.com/cloudcarver/anchor/internal/model/querier"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestAuthFlow(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	pub, priv, err := generateEd25519KeyPair()
	require.NoError(t, err)

	var (
		ctx    = context.Background()
		kid    = uuid.Must(uuid.NewRandom())
		userID = int32(1)
		rules  = []string{"read:user"}
	)

	mockStore := NewMockAuthStoreInterface(ctrl)
	auth := &Auth{
		authStore: mockStore,
	}

	mockStore.EXPECT().GetLatestKey(ctx).Return(&Key{
		ID:   kid.String(),
		Pub:  pub,
		Priv: priv,
	}, nil)

	token, err := auth.CreateToken(ctx, &querier.User{
		ID: userID,
	}, rules)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	mockStore.EXPECT().GetKeyByID(ctx, kid.String()).Return(&Key{
		ID:   kid.String(),
		Pub:  pub,
		Priv: priv,
	}, nil)

	user, err := auth.ValidateToken(ctx, token)
	require.NoError(t, err)
	require.Equal(t, userID, user.ID)
}
