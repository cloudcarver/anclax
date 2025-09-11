package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudcarver/anclax/pkg/auth"
	"github.com/cloudcarver/anclax/pkg/hooks"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
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
