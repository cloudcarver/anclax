package auth

import (
	"context"
	"time"

	"github.com/cloudcarver/anchor/pkg/config"
	"github.com/cloudcarver/anchor/pkg/hooks"
	"github.com/cloudcarver/anchor/pkg/macaroons"
	"github.com/cloudcarver/anchor/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/pkg/errors"
)

const (
	ContextKeyUserID = iota
	ContextKeyOrgID
	ContextKeyMacaroon
)

const (
	DefaultTimeoutAccessToken  = time.Minute * 10
	DefaultTimeoutRefreshToken = time.Hour * 2
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
	CreateToken(ctx context.Context, userID int32, orgID int32, caveats ...macaroons.Caveat) (int64, string, error)

	// CreateRefreshToken returns a refresh token
	CreateRefreshToken(ctx context.Context, accessKeyID int64, userID int32) (string, error)

	// ParseRefreshToken parses the given refresh token and returns the user ID
	ParseRefreshToken(ctx context.Context, refreshToken string) (int32, error)

	// InvalidateUserTokens invalidates all tokens for the given user
	InvalidateUserTokens(ctx context.Context, userID int32) error
}

type Auth struct {
	macaroonsParser     macaroons.MacaroonParserInterface
	hooks               hooks.AnchorHookInterface
	timeoutAccessToken  time.Duration
	timeoutRefreshToken time.Duration
}

// Ensure AuthService implements AuthServiceInterface
var _ AuthInterface = (*Auth)(nil)

func NewAuth(cfg *config.Config, macaroonsParser macaroons.MacaroonParserInterface, caveatParser macaroons.CaveatParserInterface, hooks hooks.AnchorHookInterface) (AuthInterface, error) {
	if err := caveatParser.Register(CaveatUserContext, func() macaroons.Caveat {
		return &UserContextCaveat{}
	}); err != nil {
		return nil, err
	}
	if err := caveatParser.Register(CaveatRefreshOnly, func() macaroons.Caveat {
		return &RefreshOnlyCaveat{}
	}); err != nil {
		return nil, err
	}

	return &Auth{
		macaroonsParser:     macaroonsParser,
		hooks:               hooks,
		timeoutAccessToken:  utils.UnwrapOrDefault(cfg.Auth.AccessExpiry, DefaultTimeoutAccessToken),
		timeoutRefreshToken: utils.UnwrapOrDefault(cfg.Auth.RefreshExpiry, DefaultTimeoutRefreshToken),
	}, nil
}

func (a *Auth) Authfunc(c *fiber.Ctx) error {
	authHeader := c.Get("Authorization")
	if authHeader == "" {
		return errors.Wrap(fiber.ErrUnauthorized, "missing authorization header")
	}

	// Remove "Bearer " prefix if present
	tokenString := authHeader
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		tokenString = authHeader[7:]
	}

	token, err := a.macaroonsParser.Parse(c.Context(), tokenString)
	if err != nil {
		return errors.Wrapf(fiber.ErrUnauthorized, "failed to parse macaroon token, token: %s, err: %v", tokenString, err)
	}

	for _, caveat := range token.Caveats() {
		if err := caveat.Validate(c); err != nil {
			return errors.Wrapf(fiber.ErrUnauthorized, "failed to validate caveat, token: %s, err: %v", tokenString, err)
		}
	}

	return nil
}

func (a *Auth) CreateToken(ctx context.Context, userID int32, orgID int32, caveats ...macaroons.Caveat) (int64, string, error) {
	token, err := a.macaroonsParser.CreateToken(ctx, userID, append(caveats, NewUserContextCaveat(userID, orgID)), a.timeoutAccessToken)
	if err != nil {
		return 0, "", errors.Wrap(err, "failed to create macaroon token")
	}

	if err := a.hooks.OnCreateToken(ctx, userID, token); err != nil {
		return 0, "", errors.Wrap(err, "failed to call hook")
	}

	return token.KeyID(), token.StringToken(), nil
}

func (a *Auth) CreateRefreshToken(ctx context.Context, accessKeyID int64, userID int32) (string, error) {
	token, err := a.macaroonsParser.CreateToken(ctx, userID, []macaroons.Caveat{
		NewRefreshOnlyCaveat(userID, accessKeyID),
	}, a.timeoutRefreshToken)
	if err != nil {
		return "", errors.Wrap(err, "failed to create macaroon token")
	}
	return token.StringToken(), nil
}

func (a *Auth) ParseRefreshToken(ctx context.Context, refreshToken string) (int32, error) {
	token, err := a.macaroonsParser.Parse(ctx, refreshToken)
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
	return a.macaroonsParser.InvalidateUserTokens(ctx, userID)
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
