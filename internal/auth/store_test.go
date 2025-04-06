package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/cloudcarver/anchor/internal/model"
	"github.com/cloudcarver/anchor/internal/model/querier"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestGetJWKByKid(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var (
		ctx      = context.Background()
		currTime = time.Now()
		expiry   = 24 * time.Hour
		kid      = uuid.Must(uuid.NewRandom())
	)

	mockModel := model.NewMockModelInterface(ctrl)
	store := &AuthStore{
		m:      mockModel,
		now:    func() time.Time { return currTime },
		expiry: expiry,
	}

	mockModel.EXPECT().GetKeyByID(ctx, kid).Return(&querier.Key{
		PublicKey:  base64.RawURLEncoding.EncodeToString(pub),
		PrivateKey: base64.RawURLEncoding.EncodeToString(priv),
		ExpiredAt:  currTime.Add(expiry),
	}, nil)

	k, err := store.GetKeyByID(ctx, kid.String())
	require.NoError(t, err)
	require.Equal(t, pub, k.Pub)
	require.Equal(t, priv, k.Priv)
}
