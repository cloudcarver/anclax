package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/auth"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/hooks"
	"github.com/cloudcarver/anclax/pkg/macaroons"
	macaroonstore "github.com/cloudcarver/anclax/pkg/macaroons/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCreateNewUser(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockAuth := auth.NewMockAuthInterface(ctrl)
	mockHooks := hooks.NewMockAnclaxHookInterface(ctrl)

	var (
		orgID  = int32(101)
		userID = int32(102)
		org    = &querier.AnclaxOrg{
			ID: orgID,
		}
		user = &querier.AnclaxUser{
			ID: userID,
		}
		username = "testuser"
		password = "testpassword"
		salt     = "salt"
		hash     = "hash"
		ctx      = context.Background()
	)

	mockModel.EXPECT().CreateOrg(ctx, fmt.Sprintf("%s's Org", username)).Return(org, nil)

	mockHooks.EXPECT().OnOrgCreated(ctx, gomock.Any(), org.ID).Return(nil)

	mockHooks.EXPECT().OnUserCreated(ctx, gomock.Any(), user.ID).Return(nil)

	mockModel.EXPECT().CreateUser(ctx, querier.CreateUserParams{
		Name:         username,
		PasswordHash: hash,
		PasswordSalt: salt,
	}).Return(user, nil)

	mockModel.EXPECT().InsertOrgOwner(ctx, querier.InsertOrgOwnerParams{
		UserID: userID,
		OrgID:  orgID,
	}).Return(nil, nil)

	mockModel.EXPECT().InsertOrgUser(ctx, querier.InsertOrgUserParams{
		UserID: userID,
		OrgID:  orgID,
	}).Return(nil, nil)

	mockModel.EXPECT().SetUserDefaultOrg(ctx, querier.SetUserDefaultOrgParams{
		UserID: userID,
		OrgID:  orgID,
	}).Return(nil)

	service := &Service{
		m:     mockModel,
		hooks: mockHooks,
		auth:  mockAuth,
		generateSaltAndHash: func(inputPassword string) (string, string, error) {
			if inputPassword != password {
				return "", "", errors.New("password mismatch")
			}
			return salt, hash, nil
		},
	}

	u, err := service.CreateNewUser(ctx, username, password)
	require.NoError(t, err)
	require.Equal(t, orgID, u.OrgID)

}

func TestUpdateUserPassword(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockAuth := auth.NewMockAuthInterface(ctrl)
	mockHooks := hooks.NewMockAnclaxHookInterface(ctrl)

	var (
		userID = int32(102)
		user   = &querier.AnclaxUser{
			ID: userID,
		}
		username = "testuser"
		password = "newpassword"
		salt     = "newsalt"
		hash     = "newhash"
		ctx      = context.Background()
	)

	mockModel.EXPECT().GetUserByName(ctx, username).Return(user, nil)

	mockModel.EXPECT().UpdateUserPassword(ctx, querier.UpdateUserPasswordParams{
		ID:           userID,
		PasswordHash: hash,
		PasswordSalt: salt,
	}).Return(nil)

	service := &Service{
		m:     mockModel,
		hooks: mockHooks,
		auth:  mockAuth,
		generateSaltAndHash: func(inputPassword string) (string, string, error) {
			if inputPassword != password {
				return "", "", errors.New("password mismatch")
			}
			return salt, hash, nil
		},
	}

	resultUserID, err := service.UpdateUserPassword(ctx, username, password)
	require.NoError(t, err)
	require.Equal(t, userID, resultUserID)
}

func TestDeleteUserByNameDeletesTokenKeysInTransaction(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)

	var (
		ctx      = context.Background()
		username = "testuser"
		userID   = int32(102)
		group    = auth.UserTokenGroup(userID)
	)

	mockModel.EXPECT().DeleteUserByNameReturningID(ctx, username).Return(userID, nil)
	mockModel.EXPECT().DeleteOpaqueKeys(ctx, &group).Return(nil)

	service := &Service{m: mockModel}

	err := service.DeleteUserByName(ctx, username)
	require.NoError(t, err)
}

func TestDeleteUserByNameNoopsWhenUserDoesNotExist(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)

	var (
		ctx      = context.Background()
		username = "missing-user"
	)

	mockModel.EXPECT().DeleteUserByNameReturningID(ctx, username).Return(int32(0), pgx.ErrNoRows)

	service := &Service{m: mockModel}

	err := service.DeleteUserByName(ctx, username)
	require.NoError(t, err)
}

func TestDeleteUserByNameReturnsTokenKeyDeleteError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)

	var (
		ctx       = context.Background()
		username  = "testuser"
		userID    = int32(102)
		group     = auth.UserTokenGroup(userID)
		deleteErr = errors.New("delete token keys failed")
	)

	mockModel.EXPECT().DeleteUserByNameReturningID(ctx, username).Return(userID, nil)
	mockModel.EXPECT().DeleteOpaqueKeys(ctx, &group).Return(deleteErr)

	service := &Service{m: mockModel}

	err := service.DeleteUserByName(ctx, username)
	require.ErrorIs(t, err, deleteErr)
}

type testKeyStore struct {
	next      int64
	keys      map[int64][]byte
	groupKeys map[string]map[int64]struct{}
}

func newTestKeyStore() *testKeyStore {
	return &testKeyStore{
		keys:      map[int64][]byte{},
		groupKeys: map[string]map[int64]struct{}{},
	}
}

func (s *testKeyStore) Create(_ context.Context, key []byte, _ time.Duration, group string) (int64, error) {
	s.next++
	keyID := s.next
	s.keys[keyID] = append([]byte(nil), key...)
	if group != "" {
		if s.groupKeys[group] == nil {
			s.groupKeys[group] = map[int64]struct{}{}
		}
		s.groupKeys[group][keyID] = struct{}{}
	}
	return keyID, nil
}

func (s *testKeyStore) Get(_ context.Context, keyID int64) ([]byte, error) {
	key, ok := s.keys[keyID]
	if !ok {
		return nil, macaroonstore.ErrKeyNotFound
	}
	return append([]byte(nil), key...), nil
}

func (s *testKeyStore) Delete(_ context.Context, keyID int64) error {
	if _, ok := s.keys[keyID]; !ok {
		return macaroonstore.ErrKeyNotFound
	}
	delete(s.keys, keyID)
	for group, keyIDs := range s.groupKeys {
		delete(keyIDs, keyID)
		if len(keyIDs) == 0 {
			delete(s.groupKeys, group)
		}
	}
	return nil
}

func (s *testKeyStore) DeleteGroupKeys(_ context.Context, group string) error {
	keyIDs, ok := s.groupKeys[group]
	if !ok {
		return macaroonstore.ErrKeyNotFound
	}
	for keyID := range keyIDs {
		delete(s.keys, keyID)
	}
	delete(s.groupKeys, group)
	return nil
}

func TestRefreshTokenRotatesRealMacaroons(t *testing.T) {
	ctx := context.Background()
	userID := int32(102)
	orgID := int32(201)
	group := auth.UserTokenGroup(userID)

	caveatParser := macaroons.NewCaveatParser()
	macaroonManager := macaroons.NewMacaroonManager(newTestKeyStore(), caveatParser)
	authSvc, err := auth.NewAuth(&config.Config{}, macaroonManager, caveatParser, nil)
	require.NoError(t, err)

	accessToken, err := authSvc.CreateToken(ctx, group, auth.DefaultTimeoutAccessToken, auth.NewUserContextCaveat(userID, orgID))
	require.NoError(t, err)

	refreshToken, err := authSvc.CreateRefreshToken(ctx, group, accessToken, auth.DefaultTimeoutRefreshToken)
	require.NoError(t, err)

	svc := &Service{
		auth:                authSvc,
		timeoutAccessToken:  auth.DefaultTimeoutAccessToken,
		timeoutRefreshToken: auth.DefaultTimeoutRefreshToken,
	}
	credentials, err := svc.RefreshToken(ctx, refreshToken.StringToken())
	require.NoError(t, err)
	require.Equal(t, apigen.Bearer, credentials.TokenType)
	require.NotEmpty(t, credentials.AccessToken)
	require.NotEmpty(t, credentials.RefreshToken)
	require.NotEqual(t, accessToken.StringToken(), credentials.AccessToken)
	require.NotEqual(t, refreshToken.StringToken(), credentials.RefreshToken)

	_, newRefreshCaveat, err := authSvc.ParseRefreshToken(ctx, credentials.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, group, newRefreshCaveat.Group)
	require.Len(t, newRefreshCaveat.AccessTokenCaveats, 1)

	_, _, err = authSvc.ParseRefreshToken(ctx, refreshToken.StringToken())
	require.Error(t, err)
}

func TestRefreshTokenParseFailureReturnsRefreshTokenExpired(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockAuth := auth.NewMockAuthInterface(ctrl)
	mockAuth.EXPECT().ParseRefreshToken(ctx, "not-a-refresh-token").Return(nil, nil, macaroons.ErrMalformedToken)

	svc := &Service{auth: mockAuth}
	credentials, err := svc.RefreshToken(ctx, "not-a-refresh-token")
	require.Nil(t, credentials)
	require.ErrorIs(t, err, ErrRefreshTokenExpired)
	require.ErrorIs(t, err, macaroons.ErrMalformedToken)
}
