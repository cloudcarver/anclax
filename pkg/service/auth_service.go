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

func (s *Service) SignIn(ctx context.Context, params apigen.SignInRequest) (*apigen.Credentials, error) {
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

	if err := s.auth.InvalidateUserTokens(ctx, user.ID); err != nil {
		return nil, errors.Wrapf(err, "failed to invalidate user tokens")
	}

	orgID, err := s.m.GetUserDefaultOrg(ctx, user.ID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get user default org")
	}

	keyID, token, err := s.auth.CreateToken(ctx, user.ID, orgID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create token")
	}

	refreshToken, err := s.auth.CreateRefreshToken(ctx, keyID, user.ID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to generate refresh token")
	}

	return &apigen.Credentials{
		AccessToken:  token,
		RefreshToken: refreshToken,
		TokenType:    apigen.Bearer,
	}, nil
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

func (s *Service) CreateNewUser(ctx context.Context, username, password string) (int32, error) {
	salt, hash, err := s.generateSaltAndHash(password)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to generate hash and salt")
	}

	var orgID int32

	if err := s.m.RunTransactionWithTx(ctx, func(tx pgx.Tx, txm model.ModelInterface) error {
		org, err := txm.CreateOrg(ctx, fmt.Sprintf("%s's Org", username))
		if err != nil {
			return errors.Wrapf(err, "failed to create organization")
		}

		orgID = org.ID

		if err := s.hooks.OnOrgCreatedWithTx(ctx, tx, org.ID); err != nil {
			return errors.Wrapf(err, "failed to run on org created hook")
		}

		user, err := txm.CreateUser(ctx, querier.CreateUserParams{
			Name:         username,
			PasswordHash: hash,
			PasswordSalt: salt,
		})
		if err != nil {
			return errors.Wrapf(err, "failed to create user")
		}

		if _, err := txm.InsertOrgOwner(ctx, querier.InsertOrgOwnerParams{
			UserID: user.ID,
			OrgID:  org.ID,
		}); err != nil {
			return errors.Wrapf(err, "failed to create organization owner")
		}

		if _, err := txm.InsertOrgUser(ctx, querier.InsertOrgUserParams{
			UserID: user.ID,
			OrgID:  org.ID,
		}); err != nil {
			return errors.Wrapf(err, "failed to create organization user")
		}

		if err := txm.SetUserDefaultOrg(ctx, querier.SetUserDefaultOrgParams{
			UserID: user.ID,
			OrgID:  org.ID,
		}); err != nil {
			return errors.Wrapf(err, "failed to set user default org")
		}
		return nil
	}); err != nil {
		return 0, errors.Wrapf(err, "failed to create new user")
	}

	return orgID, nil
}

func (s *Service) DeleteUserByName(ctx context.Context, username string) error {
	if err := s.m.DeleteUserByName(ctx, username); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return errors.Wrapf(err, "failed to delete user, username: %s", username)
	}
	return nil
}
