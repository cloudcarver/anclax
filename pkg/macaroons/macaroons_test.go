package macaroons

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/macaroons/store"
	"github.com/gofiber/fiber/v3"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

type TestCaveat struct {
	Typ      string `json:"type"`
	Data     string
	settings CaveatSettings
}

func (c *TestCaveat) Type() string {
	if c.Typ != "" {
		return c.Typ
	}
	return "test"
}

func (c *TestCaveat) Settings() CaveatSettings {
	return c.settings
}

func (c *TestCaveat) Validate(fiber.Ctx) error {
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
			&TestCaveat{Data: "caveat1", settings: CaveatSettings{AllowDuplicates: true}},
			&TestCaveat{Data: "caveat2", settings: CaveatSettings{AllowDuplicates: true}},
		}
		ttl   = time.Second * 10
		group = "user:1"
	)

	keyStore.EXPECT().Create(gomock.Any(), []byte("key"), ttl, group).Return(keyID, nil)
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

	macaroon, err := manager.CreateToken(context.Background(), caveats, ttl, group)
	require.NoError(t, err)

	parsed, err := manager.Parse(context.Background(), macaroon.StringToken())
	require.NoError(t, err)
	require.Equal(t, keyID, parsed.keyID)
	require.Equal(t, caveats, parsed.Caveats)

	third := &TestCaveat{Data: "caveat3", settings: CaveatSettings{AllowDuplicates: true}}
	require.NoError(t, macaroon.AddCaveat(third))

	encodedCaveat3, err := EncodeCaveat(third)
	require.NoError(t, err)

	caveatParser.EXPECT().Parse(encodedCaveat1).Return(caveats[0], nil)
	caveatParser.EXPECT().Parse(encodedCaveat2).Return(caveats[1], nil)
	caveatParser.EXPECT().Parse(encodedCaveat3).Return(third, nil)

	parsed, err = manager.Parse(context.Background(), macaroon.StringToken())
	require.NoError(t, err)
	require.Equal(t, append(caveats, third), parsed.Caveats)
}

func TestMacaroonDuplicatePolicy(t *testing.T) {
	first := &TestCaveat{Data: "first"}
	second := &TestCaveat{Data: "second"}
	other := &TestCaveat{Typ: "other"}
	repeatable := &TestCaveat{settings: CaveatSettings{AllowDuplicates: true}}
	tests := []struct {
		name    string
		caveats []Caveat
		reject  bool
	}{
		{name: "empty"},
		{name: "single", caveats: []Caveat{first}},
		{name: "different types", caveats: []Caveat{first, other}},
		{name: "identical values", caveats: []Caveat{first, first}, reject: true},
		{name: "different values", caveats: []Caveat{first, second}, reject: true},
		{name: "nonadjacent duplicates", caveats: []Caveat{first, other, second}, reject: true},
		{name: "duplicates allowed", caveats: []Caveat{repeatable, repeatable, repeatable}},
		{name: "only later allows duplicates", caveats: []Caveat{first, repeatable}, reject: true},
		{name: "only earlier allows duplicates", caveats: []Caveat{repeatable, first}, reject: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Run("CreateMacaroon", func(t *testing.T) {
				token, err := CreateMacaroon(1, []byte("key"), tt.caveats)
				if tt.reject {
					require.ErrorIs(t, err, ErrDuplicateCaveat)
					require.Nil(t, token)
				} else {
					require.NoError(t, err)
					require.Equal(t, tt.caveats, token.Caveats)
				}
			})
			t.Run("CreateToken", func(t *testing.T) {
				keyStore := store.NewMockKeyStore(gomock.NewController(t))
				if !tt.reject {
					keyStore.EXPECT().Create(gomock.Any(), gomock.Any(), time.Minute, "group").Return(int64(1), nil)
				}
				manager := NewMacaroonManager(keyStore, NewCaveatParser())
				token, err := manager.CreateToken(context.Background(), tt.caveats, time.Minute, "group")
				if tt.reject {
					// No key should be stored for a rejected token.
					require.ErrorIs(t, err, ErrDuplicateCaveat)
					require.Nil(t, token)
				} else {
					require.NoError(t, err)
					require.Equal(t, tt.caveats, token.Caveats)
				}
			})
			t.Run("AddCaveat", func(t *testing.T) {
				token, err := CreateMacaroon(1, []byte("key"), nil)
				require.NoError(t, err)
				for i, caveat := range tt.caveats {
					before := *token
					err := token.AddCaveat(caveat)
					if tt.reject && i == len(tt.caveats)-1 {
						require.ErrorIs(t, err, ErrDuplicateCaveat)
						require.Equal(t, &before, token, "a rejected append must not change the token")
					} else {
						require.NoError(t, err)
						require.Equal(t, tt.caveats[:i+1], token.Caveats)
					}
				}
			})
		})
	}
}

func TestMacaroonManager_ParseClientAppendedDuplicate(t *testing.T) {
	for _, allowDuplicates := range []bool{false, true} {
		for _, value := range []string{"user:1", "user:2"} {
			name := "reject/" + value
			if allowDuplicates {
				name = "allow/" + value
			}
			t.Run(name, func(t *testing.T) {
				settings := CaveatSettings{AllowDuplicates: allowDuplicates}
				parser := NewCaveatParser()
				require.NoError(t, parser.Register("test", func() Caveat {
					return &TestCaveat{settings: settings}
				}))
				key := []byte("server-key")
				keyStore := store.NewMockKeyStore(gomock.NewController(t))
				keyStore.EXPECT().Get(gomock.Any(), int64(1)).Return(key, nil).Times(2)
				manager := NewMacaroonManager(keyStore, parser)
				original, err := CreateMacaroon(1, key, []Caveat{
					&TestCaveat{Typ: "test", Data: "user:1", settings: settings},
				})
				require.NoError(t, err)

				// A client can append a caveat using only the existing signature,
				// without calling AddCaveat or knowing the server's root key.
				parts := strings.Split(original.StringToken(), ".")
				signature, err := base64.StdEncoding.DecodeString(parts[len(parts)-1])
				require.NoError(t, err)
				encoded, err := EncodeCaveat(&TestCaveat{Typ: "test", Data: value})
				require.NoError(t, err)
				signature, err = sign(signature, encoded)
				require.NoError(t, err)
				appended := strings.Join(parts[:len(parts)-1], ".") + "." + encoded + "." + base64.StdEncoding.EncodeToString(signature)

				parsed, err := manager.Parse(context.Background(), appended)
				if allowDuplicates {
					require.NoError(t, err)
					require.Len(t, parsed.Caveats, 2)
					require.Equal(t, value, parsed.Caveats[1].(*TestCaveat).Data)
				} else {
					require.ErrorIs(t, err, ErrDuplicateCaveat)
					require.Nil(t, parsed)
				}
				_, err = manager.Parse(context.Background(), original.StringToken())
				require.NoError(t, err)
			})
		}
	}
}

func TestInvalidateTokensByGroupDeletesGroupKeys(t *testing.T) {
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
		group = "user:1"
	)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			keyStore.EXPECT().DeleteGroupKeys(gomock.Any(), group).Return(tc.err)

			manager := &MacaroonsManager{
				keyStore: keyStore,
			}

			err := manager.InvalidateTokensByGroup(context.Background(), group)
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
