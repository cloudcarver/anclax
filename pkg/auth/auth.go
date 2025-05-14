package auth

import (
	"context"
	"time"

	"github.com/cloudcarver/anchor/pkg/macaroons"
	"github.com/gofiber/fiber/v2"
	"github.com/pkg/errors"
)

const (
	ContextKeyUserID = iota
	ContextKeyOrgID
	ContextKeyMacaroon
)

const (
	TimeoutAccessToken  = time.Minute * 10
	TimeoutRefreshToken = time.Hour * 2
)

var ErrUserIdentityNotExist = errors.New("user identity not exists")

type User struct {
	ID             int32
	OrganizationID int32
	AccessRules    map[string]struct{}
}

type AuthInterface interface {
	Authfunc(c *fiber.Ctx) error

	// CreateToken creates a macaroon token for the given user, the userID is required to track all generated keys.
	// When the user logout, all keys will be invalidated.
	CreateToken(ctx context.Context, userID int32, caveats ...macaroons.Caveat) (int64, string, error)

	// CreateRefreshToken returns a refresh token
	CreateRefreshToken(ctx context.Context, accessKeyID int64, userID int32) (string, error)

	// ParseRefreshToken parses the given refresh token and returns the user ID
	ParseRefreshToken(ctx context.Context, refreshToken string) (int32, error)

	// InvalidateUserTokens invalidates all tokens for the given user
	InvalidateUserTokens(ctx context.Context, userID int32) error
}

type Auth struct {
	macaroons macaroons.MacaroonManagerInterface
}

// Ensure AuthService implements AuthServiceInterface
var _ AuthInterface = (*Auth)(nil)

func NewAuth(macaroons macaroons.MacaroonManagerInterface) (AuthInterface, error) {
	return &Auth{
		macaroons: macaroons,
	}, nil
}

func (a *Auth) Authfunc(c *fiber.Ctx) error {
	authHeader := c.Get("Authorization")
	if authHeader == "" {
		return errors.New("missing authorization header")
	}

	// Remove "Bearer " prefix if present
	tokenString := authHeader
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		tokenString = authHeader[7:]
	}

	token, err := a.macaroons.Parse(c.Context(), tokenString)
	if err != nil {
		return errors.Wrapf(err, "failed to parse macaroon token, token: %s", tokenString)
	}

	for _, caveat := range token.Caveats() {
		if err := caveat.Validate(c); err != nil {
			return errors.Wrapf(err, "failed to validate macaroon token, caveat: %s, token: %s", caveat.Type(), tokenString)
		}
	}

	return nil
}

func (a *Auth) CreateToken(ctx context.Context, userID int32, caveats ...macaroons.Caveat) (int64, string, error) {
	token, err := a.macaroons.CreateToken(ctx, userID, append(caveats, NewUserContextCaveat(userID)), TimeoutAccessToken)
	if err != nil {
		return 0, "", errors.Wrap(err, "failed to create macaroon token")
	}
	return token.KeyID(), token.StringToken(), nil
}

func (a *Auth) CreateRefreshToken(ctx context.Context, accessKeyID int64, userID int32) (string, error) {
	token, err := a.macaroons.CreateToken(ctx, userID, []macaroons.Caveat{
		NewRefreshOnlyCaveat(userID, accessKeyID),
	}, TimeoutRefreshToken)
	if err != nil {
		return "", errors.Wrap(err, "failed to create macaroon token")
	}
	return token.StringToken(), nil
}

func (a *Auth) ParseRefreshToken(ctx context.Context, refreshToken string) (int32, error) {
	token, err := a.macaroons.Parse(ctx, refreshToken)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to parse macaroon token, token: %s", refreshToken)
	}

	for _, caveat := range token.Caveats() {
		if caveat.Type() == CaveatRefreshOnly {
			roc, ok := caveat.(*RefreshOnlyCaveat)
			if !ok {
				return 0, errors.Errorf("caveat is not a RefreshOnlyCaveat even though it has type %s", CaveatRefreshOnly)
			}
			return roc.UserID, nil
		}
	}

	return 0, errors.New("no userID found in refresh token")
}

func (a *Auth) InvalidateUserTokens(ctx context.Context, userID int32) error {
	return a.macaroons.InvalidateUserTokens(ctx, userID)
}

func GetUserID(c *fiber.Ctx) (int32, error) {
	userID, ok := c.Locals(ContextKeyUserID).(int32)
	if !ok {
		return 0, ErrUserIdentityNotExist
	}
	return userID, nil
}

func GetOrgID(c *fiber.Ctx) (int32, error) {
	orgID, ok := c.Locals(ContextKeyOrgID).(int32)
	if !ok {
		return 0, ErrUserIdentityNotExist
	}
	return orgID, nil
}
