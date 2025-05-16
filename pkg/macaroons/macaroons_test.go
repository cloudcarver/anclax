package macaroons

import (
	"context"
	"testing"
	"time"

	"github.com/cloudcarver/anchor/pkg/macaroons/store"
	store_mock "github.com/cloudcarver/anchor/pkg/macaroons/store/mock"
	"github.com/gofiber/fiber/v2"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

type TestCaveat struct {
	Data string
}

func (c *TestCaveat) Encode() (string, error) {
	return c.Data, nil
}

func (c *TestCaveat) Decode(s string) error {
	c.Data = s
	return nil
}

func (c *TestCaveat) Type() string {
	return "test"
}

func (c *TestCaveat) Validate(*fiber.Ctx) error {
	return nil
}

func TestMacaroonManager_CreateMacaroon(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	keyStore := store_mock.NewMockKeyStore(ctrl)
	caveatParser := NewMockCaveatParserInterface(ctrl)

	var (
		keyID   = int64(9527)
		caveats = []Caveat{
			&TestCaveat{Data: "caveat1"},
			&TestCaveat{Data: "caveat2"},
		}
		ttl    = time.Second * 10
		userID = int32(1)
	)

	keyStore.EXPECT().Create(gomock.Any(), userID, []byte("key"), ttl).Return(keyID, nil)
	keyStore.EXPECT().Get(gomock.Any(), keyID).Return([]byte("key"), nil)

	encodedCaveat1, err := EncodeCaveat(caveats[0])
	require.NoError(t, err)
	encodedCaveat2, err := EncodeCaveat(caveats[1])
	require.NoError(t, err)

	caveatParser.EXPECT().Parse(encodedCaveat1).Return(caveats[0], nil)
	caveatParser.EXPECT().Parse(encodedCaveat2).Return(caveats[1], nil)

	manager := &MacaroonsParser{
		keyStore:     keyStore,
		caveatParser: caveatParser,
		randomKey:    func() ([]byte, error) { return []byte("key"), nil },
	}

	macaroon, err := manager.CreateToken(context.Background(), userID, caveats, ttl)
	require.NoError(t, err)

	parsed, err := manager.Parse(context.Background(), macaroon.StringToken())
	require.NoError(t, err)
	require.Equal(t, keyID, parsed.keyID)
	require.Equal(t, caveats, parsed.caveats)
}

func TestInvalidateUserTokens(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	keyStore := store_mock.NewMockKeyStore(ctrl)

	var testCases = []struct {
		name string
		err  error
	}{
		{
			name: "success",
			err:  nil,
		},
		{
			name: "error",
			err:  errors.New("error"),
		},
		{
			name: "key not found",
			err:  store.ErrKeyNotFound,
		},
	}

	var (
		userID = int32(1)
	)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			keyStore.EXPECT().DeleteUserKeys(gomock.Any(), userID).Return(tc.err)

			manager := &MacaroonsParser{
				keyStore: keyStore,
			}

			err := manager.InvalidateUserTokens(context.Background(), userID)
			if tc.err == nil {
				require.NoError(t, err)
			} else if tc.err == store.ErrKeyNotFound {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
