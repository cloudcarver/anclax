package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/cloudcarver/anchor/pkg/auth"
	"github.com/cloudcarver/anchor/pkg/hooks"
	"github.com/cloudcarver/anchor/pkg/zcore/model"
	"github.com/cloudcarver/anchor/pkg/zgen/querier"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCreateNewUser(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := model.NewMockModelInterfaceWithTransaction(ctrl)
	mockAuth := auth.NewMockAuthInterface(ctrl)
	mockHooks := hooks.NewMockAnchorHookInterface(ctrl)

	var (
		orgID  = int32(101)
		userID = int32(102)
		org    = &querier.AnchorOrg{
			ID: orgID,
		}
		user = &querier.AnchorUser{
			ID: userID,
		}
		username = "testuser"
		password = "testpassword"
		salt     = "salt"
		hash     = "hash"
		ctx      = context.Background()
	)

	mockModel.EXPECT().CreateOrg(ctx, fmt.Sprintf("%s's Org", username)).Return(org, nil)

	mockHooks.EXPECT().OnOrgCreatedWithTx(ctx, gomock.Any(), org.ID).Return(nil)

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

	orgID, err := service.CreateNewUser(ctx, username, password)
	require.NoError(t, err)
	require.Equal(t, orgID, org.ID)

}
