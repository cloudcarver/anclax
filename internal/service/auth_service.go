package service

import (
	"context"
	"fmt"

	"github.com/cloudcarver/anchor/internal/apigen"
	"github.com/cloudcarver/anchor/internal/model"
	"github.com/cloudcarver/anchor/internal/model/querier"
	"github.com/cloudcarver/anchor/internal/utils"
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
	token, err := s.auth.CreateToken(ctx, user, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create token")
	}
	refreshToken, err := s.auth.CreateRefreshToken(ctx, user.ID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to generate refresh token")
	}

	return &apigen.Credentials{
		AccessToken:  token,
		RefreshToken: refreshToken,
		TokenType:    apigen.Bearer,
	}, nil
}

func (s *Service) CreateToken(ctx context.Context, userID int32) (*apigen.Credentials, error) {
	newRefreshToken, err := s.auth.CreateRefreshToken(ctx, userID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to generate refresh token")
	}
	user, err := s.m.GetUser(ctx, userID)
	if err != nil {
		return nil, errors.Wrapf(err, "")
	}

	accessToken, err := s.auth.CreateToken(ctx, user, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create token")
	}

	return &apigen.Credentials{
		AccessToken:  accessToken,
		RefreshToken: newRefreshToken,
		TokenType:    apigen.Bearer,
	}, nil
}

func (s *Service) CreateNewUser(ctx context.Context, username, password string) (int32, error) {
	salt, hash, err := s.generateHashAndSalt(password)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to generate hash and salt")
	}
	var orgID int32
	if err := s.m.RunTransaction(ctx, func(txm model.ModelInterface) error {
		org, err := txm.CreateOrganization(ctx, fmt.Sprintf("%s's Org", username))
		if err != nil {
			return errors.Wrapf(err, "failed to create organization")
		}
		user, err := txm.CreateUser(ctx, querier.CreateUserParams{
			Name:           username,
			PasswordHash:   hash,
			PasswordSalt:   salt,
			OrganizationID: org.ID,
		})
		if err := txm.CreateOrganizationOwner(ctx, querier.CreateOrganizationOwnerParams{
			UserID:         user.ID,
			OrganizationID: org.ID,
		}); err != nil {
			return errors.Wrapf(err, "failed to create organization owner")
		}
		if err != nil {
			return errors.Wrapf(err, "failed to create user")
		}
		orgID = org.ID
		return nil
	}); err != nil {
		return 0, err
	}
	return orgID, nil
}
