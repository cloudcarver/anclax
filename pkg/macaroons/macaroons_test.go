package macaroons

import (
	"context"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/macaroons/store"
	"github.com/gofiber/fiber/v2"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

type TestCaveat struct {
	Data string
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

	keyStore := store.NewMockKeyStore(ctrl)
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
	keyStore.EXPECT().Get(gomock.Any(), keyID).Return([]byte("key"), nil).Times(2)

	encodedCaveat1, err := EncodeCaveat(caveats[0])
	require.NoError(t, err)
	encodedCaveat2, err := EncodeCaveat(caveats[1])
	require.NoError(t, err)

	caveatParser.EXPECT().Parse(encodedCaveat1).Return(caveats[0], nil)
	caveatParser.EXPECT().Parse(encodedCaveat2).Return(caveats[1], nil)

	manager := &MacaroonsManager{
		keyStore:     keyStore,
		caveatParser: caveatParser,
		randomKey:    func() ([]byte, error) { return []byte("key"), nil },
	}

	macaroon, err := manager.CreateToken(context.Background(), caveats, ttl, &userID)
	require.NoError(t, err)

	parsed, err := manager.Parse(context.Background(), macaroon.StringToken())
	require.NoError(t, err)
	require.Equal(t, keyID, parsed.keyID)
	require.Equal(t, caveats, parsed.Caveats)

	macaroon.AddCaveat(&TestCaveat{Data: "caveat3"})
	require.NoError(t, err)

	encodedCaveat3, err := EncodeCaveat(&TestCaveat{Data: "caveat3"})
	require.NoError(t, err)

	caveatParser.EXPECT().Parse(encodedCaveat1).Return(caveats[0], nil)
	caveatParser.EXPECT().Parse(encodedCaveat2).Return(caveats[1], nil)
	caveatParser.EXPECT().Parse(encodedCaveat3).Return(&TestCaveat{Data: "caveat3"}, nil)

	parsed, err = manager.Parse(context.Background(), macaroon.StringToken())
	require.NoError(t, err)
	require.Equal(t, append(caveats, &TestCaveat{Data: "caveat3"}), parsed.Caveats)
}

func TestInvalidateUserTokens(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	keyStore := store.NewMockKeyStore(ctrl)

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

			manager := &MacaroonsManager{
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

func TestChainedHmac(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		key            = []byte("key")
		keyID          = "9527"
		encodedCaveats = []string{"caveat1", "caveat2", "caveat3"}
	)

	signature, err := chainedHmac(key, keyID, encodedCaveats)
	require.NoError(t, err)

	s1, err := sign(key, keyID)
	require.NoError(t, err)
	s2, err := sign(s1, encodedCaveats[0])
	require.NoError(t, err)
	s3, err := sign(s2, encodedCaveats[1])
	require.NoError(t, err)
	s4, err := sign(s3, encodedCaveats[2])
	require.NoError(t, err)
	require.Equal(t, signature, s4)

	v1, err := chainedHmac(key, keyID, encodedCaveats[:2])
	require.NoError(t, err)
	require.Equal(t, s3, v1)

	v2, err := sign(v1, "caveat3")
	require.NoError(t, err)
	require.Equal(t, signature, v2)
}
