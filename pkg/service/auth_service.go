package service

import (
	"context"
	"fmt"

	"github.com/cloudcarver/anchor/pkg/utils"
	"github.com/cloudcarver/anchor/pkg/zcore/model"
	"github.com/cloudcarver/anchor/pkg/zgen/apigen"
	"github.com/cloudcarver/anchor/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
)

func (s *Service) SignIn(ctx context.Context, userID int32) (*apigen.Credentials, error) {
	if s.singleSession {
		if err := s.auth.InvalidateUserTokens(ctx, userID); err != nil {
			return nil, errors.Wrapf(err, "failed to invalidate user tokens")
		}
	}

	orgID, err := s.m.GetUserDefaultOrg(ctx, userID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get user default org")
	}

	keyID, token, err := s.auth.CreateToken(ctx, userID, orgID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create token")
	}

	refreshToken, err := s.auth.CreateRefreshToken(ctx, keyID, userID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to generate refresh token")
	}

	return &apigen.Credentials{
		AccessToken:  token,
		RefreshToken: refreshToken,
		TokenType:    apigen.Bearer,
	}, nil
}

func (s *Service) SignInWithPassword(ctx context.Context, params apigen.SignInRequest) (*apigen.Credentials, error) {
	user, err := s.m.GetUserByName(ctx, params.Name)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, errors.Wrapf(ErrUserNotFound, "user %s not found", params.Name)
		}
		return nil, errors.Wrapf(err, "failed to get user by name")
	}
	input, err := utils.HashPassword(params.Password, user.PasswordSalt)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to hash password")
	}
	if input != user.PasswordHash {
		return nil, ErrInvalidPassword
	}

	return s.SignIn(ctx, user.ID)
}

func (s *Service) RefreshToken(ctx context.Context, userID int32, refreshToken string) (*apigen.Credentials, error) {
	user, err := s.m.GetUser(ctx, userID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get user by id: %d", userID)
	}
	if err := s.auth.InvalidateUserTokens(ctx, userID); err != nil {
		return nil, errors.Wrapf(err, "failed to invalidate user tokens")
	}

	orgID, err := s.m.GetUserDefaultOrg(ctx, userID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get user default org")
	}

	keyID, accessToken, err := s.auth.CreateToken(ctx, user.ID, orgID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create token")
	}

	newRefreshToken, err := s.auth.CreateRefreshToken(ctx, keyID, userID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to generate refresh token")
	}

	return &apigen.Credentials{
		AccessToken:  accessToken,
		RefreshToken: newRefreshToken,
		TokenType:    apigen.Bearer,
	}, nil
}

func (s *Service) GetUserIDByUsername(ctx context.Context, username string) (int32, error) {
	user, err := s.m.GetUserByName(ctx, username)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, errors.Wrapf(ErrUserNotFound, "user %s not found", username)
		}
		return 0, errors.Wrapf(err, "failed to get user by name")
	}
	return user.ID, nil
}

func (s *Service) CreateNewUser(ctx context.Context, username, password string) (int32, error) {
	var orgID int32
	if err := s.m.RunTransactionWithTx(ctx, func(tx pgx.Tx, txm model.ModelInterface) error {
		id, err := s.CreateNewUserWithTx(ctx, tx, username, password)
		orgID = id
		return err
	}); err != nil {
		return 0, errors.Wrapf(err, "failed to create new user")
	}
	return orgID, nil
}

func (s *Service) CreateNewUserWithTx(ctx context.Context, tx pgx.Tx, username, password string) (int32, error) {
	salt, hash, err := s.generateSaltAndHash(password)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to generate hash and salt")
	}

	txm := s.m.SpawnWithTx(tx)

	org, err := txm.CreateOrg(ctx, fmt.Sprintf("%s's Org", username))
	if err != nil {
		return 0, errors.Wrapf(err, "failed to create organization")
	}

	if err := s.hooks.OnOrgCreated(ctx, tx, org.ID); err != nil {
		return 0, errors.Wrapf(err, "failed to run on org created hook")
	}

	user, err := txm.CreateUser(ctx, querier.CreateUserParams{
		Name:         username,
		PasswordHash: hash,
		PasswordSalt: salt,
	})
	if err != nil {
		return 0, errors.Wrapf(err, "failed to create user")
	}

	if err := s.hooks.OnUserCreated(ctx, tx, user.ID); err != nil {
		return 0, errors.Wrapf(err, "failed to run on user created hook")
	}

	if _, err := txm.InsertOrgOwner(ctx, querier.InsertOrgOwnerParams{
		UserID: user.ID,
		OrgID:  org.ID,
	}); err != nil {
		return 0, errors.Wrapf(err, "failed to create organization owner")
	}

	if _, err := txm.InsertOrgUser(ctx, querier.InsertOrgUserParams{
		UserID: user.ID,
		OrgID:  org.ID,
	}); err != nil {
		return 0, errors.Wrapf(err, "failed to create organization user")
	}

	if err := txm.SetUserDefaultOrg(ctx, querier.SetUserDefaultOrgParams{
		UserID: user.ID,
		OrgID:  org.ID,
	}); err != nil {
		return 0, errors.Wrapf(err, "failed to set user default org")
	}

	return org.ID, nil
}

func (s *Service) DeleteUserByName(ctx context.Context, username string) error {
	return s.m.DeleteUserByName(ctx, username)
}

func (s *Service) RestoreUserByName(ctx context.Context, username string) error {
	return s.m.RestoreUserByName(ctx, username)
}

func (s *Service) CreateTestAccount(ctx context.Context, username, password string) (int32, error) {
	user, err := s.m.GetUserByName(ctx, username)
	if err != nil && err != pgx.ErrNoRows {
		return 0, errors.Wrapf(err, "failed to get user by name")
	}

	if err == nil {
		return user.ID, nil
	}

	return s.CreateNewUser(ctx, username, password)
}

func (s *Service) UpdateUserPassword(ctx context.Context, username, password string) (int32, error) {
	user, err := s.m.GetUserByName(ctx, username)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to get user by name")
	}

	salt, hash, err := s.generateSaltAndHash(password)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to generate hash and salt")
	}

	if err := s.m.UpdateUserPassword(ctx, querier.UpdateUserPasswordParams{
		ID:           user.ID,
		PasswordHash: hash,
		PasswordSalt: salt,
	}); err != nil {
		return 0, errors.Wrapf(err, "failed to update user password")
	}

	return user.ID, nil
}

func (s *Service) IsUsernameExists(ctx context.Context, username string) (bool, error) {
	return s.m.IsUsernameExists(ctx, username)
}
