package service

import (
	"bytes"
	"context"
	"fmt"
	"sync"
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
	"github.com/jackc/pgx/v5/pgconn"
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

func TestCreateNewUserMapsOnlyUsernameUniqueConstraint(t *testing.T) {
	tests := []struct {
		name            string
		pgErr           *pgconn.PgError
		wantUsernameErr bool
	}{
		{
			name: "username unique violation",
			pgErr: &pgconn.PgError{
				Code:           postgresUniqueViolation,
				ConstraintName: usernameUniqueConstraint,
			},
			wantUsernameErr: true,
		},
		{
			name: "different unique violation",
			pgErr: &pgconn.PgError{
				Code:           postgresUniqueViolation,
				ConstraintName: "some_other_unique_constraint",
			},
		},
		{
			name: "different postgres error",
			pgErr: &pgconn.PgError{
				Code:           "23503",
				ConstraintName: usernameUniqueConstraint,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)
			mockHooks := hooks.NewMockAnclaxHookInterface(ctrl)
			ctx := context.Background()
			org := &querier.AnclaxOrg{ID: 101}

			mockModel.EXPECT().CreateOrg(ctx, "duplicate's Org").Return(org, nil)
			mockHooks.EXPECT().OnOrgCreated(ctx, gomock.Any(), org.ID).Return(nil)
			mockModel.EXPECT().CreateUser(ctx, querier.CreateUserParams{
				Name:         "duplicate",
				PasswordHash: "hash",
				PasswordSalt: "salt",
			}).Return(nil, tt.pgErr)

			svc := &Service{
				m:     mockModel,
				hooks: mockHooks,
				generateSaltAndHash: func(string) (string, string, error) {
					return "salt", "hash", nil
				},
			}
			user, err := svc.CreateNewUser(ctx, "duplicate", "password")
			require.Nil(t, user)
			require.Error(t, err)
			if tt.wantUsernameErr {
				require.ErrorIs(t, err, ErrUsernameExists)
			} else {
				require.NotErrorIs(t, err, ErrUsernameExists)
			}
		})
	}
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

type testKeyRecord struct {
	key       []byte
	expiresAt time.Time
}

type testKeyStoreState struct {
	next           int64
	keys           map[int64]testKeyRecord
	groupKeys      map[string]map[int64]struct{}
	createAttempts int
	failCreateAt   int
}

type testKeyStore struct {
	mu    *sync.Mutex
	state *testKeyStoreState
	now   func() time.Time
	inTx  bool
}

func newTestKeyStore() *testKeyStore {
	return &testKeyStore{
		mu: &sync.Mutex{},
		state: &testKeyStoreState{
			keys:      map[int64]testKeyRecord{},
			groupKeys: map[string]map[int64]struct{}{},
		},
		now: time.Now,
	}
}

func (s *testKeyStore) RunTransaction(_ context.Context, f func(macaroonstore.KeyStore) error) error {
	if s.inTx {
		return f(s)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := cloneTestKeyStoreState(s.state)
	txStore := &testKeyStore{mu: s.mu, state: s.state, now: s.now, inTx: true}
	if err := f(txStore); err != nil {
		*s.state = *snapshot
		return err
	}
	return nil
}

func cloneTestKeyStoreState(state *testKeyStoreState) *testKeyStoreState {
	clone := &testKeyStoreState{
		next:           state.next,
		keys:           make(map[int64]testKeyRecord, len(state.keys)),
		groupKeys:      make(map[string]map[int64]struct{}, len(state.groupKeys)),
		createAttempts: state.createAttempts,
		failCreateAt:   state.failCreateAt,
	}
	for keyID, record := range state.keys {
		record.key = append([]byte(nil), record.key...)
		clone.keys[keyID] = record
	}
	for group, keyIDs := range state.groupKeys {
		clone.groupKeys[group] = make(map[int64]struct{}, len(keyIDs))
		for keyID := range keyIDs {
			clone.groupKeys[group][keyID] = struct{}{}
		}
	}
	return clone
}

func (s *testKeyStore) lock() func() {
	if s.inTx {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func (s *testKeyStore) Create(_ context.Context, key []byte, ttl time.Duration, group string) (int64, error) {
	unlock := s.lock()
	defer unlock()

	s.state.createAttempts++
	if s.state.failCreateAt > 0 && s.state.createAttempts == s.state.failCreateAt {
		return 0, errors.New("injected create failure")
	}

	s.state.next++
	keyID := s.state.next
	s.state.keys[keyID] = testKeyRecord{
		key:       append([]byte(nil), key...),
		expiresAt: s.now().Add(ttl),
	}
	if group != "" {
		if s.state.groupKeys[group] == nil {
			s.state.groupKeys[group] = map[int64]struct{}{}
		}
		s.state.groupKeys[group][keyID] = struct{}{}
	}
	return keyID, nil
}

func (s *testKeyStore) Get(_ context.Context, keyID int64) ([]byte, error) {
	unlock := s.lock()
	defer unlock()

	record, ok := s.state.keys[keyID]
	if !ok || !record.expiresAt.After(s.now()) {
		return nil, macaroonstore.ErrKeyNotFound
	}
	return append([]byte(nil), record.key...), nil
}

func (s *testKeyStore) Consume(_ context.Context, keyID int64, key []byte) error {
	unlock := s.lock()
	defer unlock()

	record, ok := s.state.keys[keyID]
	if !ok || !record.expiresAt.After(s.now()) || !bytes.Equal(record.key, key) {
		return macaroonstore.ErrKeyNotFound
	}
	s.deleteLocked(keyID)
	return nil
}

func (s *testKeyStore) Delete(_ context.Context, keyID int64) error {
	unlock := s.lock()
	defer unlock()

	if _, ok := s.state.keys[keyID]; !ok {
		return macaroonstore.ErrKeyNotFound
	}
	s.deleteLocked(keyID)
	return nil
}

func (s *testKeyStore) deleteLocked(keyID int64) {
	delete(s.state.keys, keyID)
	for group, keyIDs := range s.state.groupKeys {
		delete(keyIDs, keyID)
		if len(keyIDs) == 0 {
			delete(s.state.groupKeys, group)
		}
	}
}

func (s *testKeyStore) DeleteGroupKeys(_ context.Context, group string) error {
	unlock := s.lock()
	defer unlock()

	keyIDs, ok := s.state.groupKeys[group]
	if !ok {
		return macaroonstore.ErrKeyNotFound
	}
	for keyID := range keyIDs {
		delete(s.state.keys, keyID)
	}
	delete(s.state.groupKeys, group)
	return nil
}

func (s *testKeyStore) failCreateAfter(additionalAttempts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.failCreateAt = s.state.createAttempts + additionalAttempts
}

func TestRefreshTokenRotatesRealMacaroons(t *testing.T) {
	ctx := context.Background()
	userID := int32(102)
	orgID := int32(201)
	group := auth.UserTokenGroup(userID)

	caveatParser := macaroons.NewCaveatParser()
	keyStore := newTestKeyStore()
	macaroonManager := macaroons.NewMacaroonManager(keyStore, caveatParser)
	authSvc, err := auth.NewAuth(&config.Config{}, macaroonManager, caveatParser, nil)
	require.NoError(t, err)

	accessToken, err := authSvc.CreateToken(ctx, group, auth.DefaultTimeoutAccessToken, auth.NewUserContextCaveat(userID, orgID))
	require.NoError(t, err)

	refreshToken, err := authSvc.CreateRefreshToken(ctx, group, accessToken, auth.DefaultTimeoutRefreshToken)
	require.NoError(t, err)

	svc := &Service{
		auth: authSvc,
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

func TestRefreshTokenConcurrentReplayHasOneWinner(t *testing.T) {
	ctx := context.Background()
	userID := int32(102)
	orgID := int32(201)
	group := auth.UserTokenGroup(userID)

	caveatParser := macaroons.NewCaveatParser()
	keyStore := newTestKeyStore()
	macaroonManager := macaroons.NewMacaroonManager(keyStore, caveatParser)
	authSvc, err := auth.NewAuth(&config.Config{}, macaroonManager, caveatParser, nil)
	require.NoError(t, err)

	accessToken, err := authSvc.CreateToken(ctx, group, auth.DefaultTimeoutAccessToken, auth.NewUserContextCaveat(userID, orgID))
	require.NoError(t, err)
	refreshToken, err := authSvc.CreateRefreshToken(ctx, group, accessToken, auth.DefaultTimeoutRefreshToken)
	require.NoError(t, err)

	svc := &Service{auth: authSvc}
	const contenders = 16
	start := make(chan struct{})
	type result struct {
		credentials *apigen.Credentials
		err         error
	}
	results := make(chan result, contenders)
	for range contenders {
		go func() {
			<-start
			credentials, err := svc.RefreshToken(ctx, refreshToken.StringToken())
			results <- result{credentials: credentials, err: err}
		}()
	}
	close(start)

	var winner *apigen.Credentials
	losers := 0
	for range contenders {
		result := <-results
		if result.err == nil {
			require.Nil(t, winner, "more than one refresh request succeeded")
			winner = result.credentials
			continue
		}
		require.ErrorIs(t, result.err, ErrRefreshTokenExpired)
		losers++
	}
	require.NotNil(t, winner)
	require.Equal(t, contenders-1, losers)

	_, _, err = authSvc.ParseRefreshToken(ctx, winner.RefreshToken)
	require.NoError(t, err)
	_, _, err = authSvc.ParseRefreshToken(ctx, refreshToken.StringToken())
	require.Error(t, err)
}

func TestRefreshTokenRotationFailureRollsBackConsumption(t *testing.T) {
	ctx := context.Background()
	userID := int32(102)
	orgID := int32(201)
	group := auth.UserTokenGroup(userID)

	caveatParser := macaroons.NewCaveatParser()
	keyStore := newTestKeyStore()
	macaroonManager := macaroons.NewMacaroonManager(keyStore, caveatParser)
	authSvc, err := auth.NewAuth(&config.Config{}, macaroonManager, caveatParser, nil)
	require.NoError(t, err)

	accessToken, err := authSvc.CreateToken(ctx, group, auth.DefaultTimeoutAccessToken, auth.NewUserContextCaveat(userID, orgID))
	require.NoError(t, err)
	refreshToken, err := authSvc.CreateRefreshToken(ctx, group, accessToken, auth.DefaultTimeoutRefreshToken)
	require.NoError(t, err)

	// Fail the second replacement creation, after the old token has been
	// consumed and the replacement access key has been written.
	keyStore.failCreateAfter(2)
	svc := &Service{auth: authSvc}
	credentials, err := svc.RefreshToken(ctx, refreshToken.StringToken())
	require.Nil(t, credentials)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRefreshTokenExpired)

	_, _, err = authSvc.ParseRefreshToken(ctx, refreshToken.StringToken())
	require.NoError(t, err, "failed rotation must restore the original refresh token")
	_, err = macaroonManager.Parse(ctx, accessToken.StringToken())
	require.NoError(t, err, "failed rotation must restore the original access token")
}

func TestRefreshTokenParseFailureReturnsRefreshTokenExpired(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockAuth := auth.NewMockAuthInterface(ctrl)
	mockAuth.EXPECT().RotateRefreshToken(ctx, "not-a-refresh-token").Return(nil, nil, fmt.Errorf("%w: %w", auth.ErrInvalidRefreshToken, macaroons.ErrMalformedToken))

	svc := &Service{auth: mockAuth}
	credentials, err := svc.RefreshToken(ctx, "not-a-refresh-token")
	require.Nil(t, credentials)
	require.ErrorIs(t, err, ErrRefreshTokenExpired)
	require.ErrorIs(t, err, macaroons.ErrMalformedToken)
}
