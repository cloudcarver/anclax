package auth

import (
	"context"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/hooks"
	"github.com/cloudcarver/anclax/pkg/macaroons"
	"github.com/cloudcarver/anclax/pkg/utils"
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

var (
	ErrUserIdentityNotExist = errors.New("user identity not exists")
	ErrInvalidRefreshToken  = errors.New("invalid refresh token")
)

type User struct {
	ID             int32
	OrganizationID int32
	AccessRules    map[string]struct{}
}

type AuthInterface interface {
	Authfunc(c *fiber.Ctx) error

	// CreateTokenWithRefreshToken creates both access token and refresh token
	CreateUserTokens(ctx context.Context, userID int32, orgID int32, caveats ...macaroons.Caveat) (*macaroons.Macaroon, *macaroons.Macaroon, error)

	// CreateToken creates a macaroon token, the userID is required to track all generated keys.
	CreateToken(ctx context.Context, userID *int32, caveats ...macaroons.Caveat) (*macaroons.Macaroon, error)

	// CreateRefreshToken creates a refresh token for the given userID and access token
	CreateRefreshToken(ctx context.Context, userID *int32, accessToken *macaroons.Macaroon) (*macaroons.Macaroon, error)

	// ParseRefreshToken parses the given refresh token and returns the carrying info
	ParseRefreshToken(ctx context.Context, refreshToken string) (*macaroons.Macaroon, *RefreshOnlyCaveat, error)

	// InvalidateUserTokens invalidates all tokens for the given user
	InvalidateUserTokens(ctx context.Context, userID int32) error

	// InvalidateToken invalidates the token with the given key ID
	InvalidateToken(ctx context.Context, keyID int64) error
}

type Auth struct {
	macaroonManager     macaroons.MacaroonManagerInterface
	hooks               hooks.AnclaxHookInterface
	timeoutAccessToken  time.Duration
	timeoutRefreshToken time.Duration
}

// Ensure AuthService implements AuthServiceInterface
var _ AuthInterface = (*Auth)(nil)

func NewAuth(cfg *config.Config, macaroonManager macaroons.MacaroonManagerInterface, caveatParser macaroons.CaveatParserInterface, hooks hooks.AnclaxHookInterface) (AuthInterface, error) {
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
		macaroonManager:     macaroonManager,
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

	token, err := a.macaroonManager.Parse(c.Context(), tokenString)
	if err != nil {
		return errors.Wrapf(fiber.ErrUnauthorized, "failed to parse macaroon token, token: %s, err: %v", tokenString, err)
	}

	c.Locals(ContextKeyMacaroon, token)

	for _, caveat := range token.Caveats {
		if err := caveat.Validate(c); err != nil {
			return errors.Wrapf(fiber.ErrUnauthorized, "failed to validate caveat, token: %s, err: %v", tokenString, err)
		}
	}

	return nil
}

func (a *Auth) CreateUserTokens(ctx context.Context, userID int32, orgID int32, caveats ...macaroons.Caveat) (*macaroons.Macaroon, *macaroons.Macaroon, error) {
	accessToken, err := a.macaroonManager.CreateToken(ctx, append(caveats, NewUserContextCaveat(userID, orgID)), a.timeoutAccessToken, &userID)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create macaroon token")
	}

	refreshToken, err := a.CreateRefreshToken(ctx, &userID, accessToken)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create refresh token")
	}

	if err := a.hooks.OnUserTokensCreated(ctx, userID, accessToken); err != nil {
		return nil, nil, errors.Wrap(err, "failed to call hook")
	}

	return accessToken, refreshToken, nil
}

func (a *Auth) CreateToken(ctx context.Context, userID *int32, caveats ...macaroons.Caveat) (*macaroons.Macaroon, error) {
	token, err := a.macaroonManager.CreateToken(ctx, caveats, a.timeoutAccessToken, userID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create macaroon token")
	}
	return token, nil
}

func (a *Auth) CreateRefreshToken(ctx context.Context, userID *int32, accessToken *macaroons.Macaroon) (*macaroons.Macaroon, error) {
	token, err := a.macaroonManager.CreateToken(ctx, []macaroons.Caveat{
		NewRefreshOnlyCaveat(userID, accessToken),
	}, a.timeoutRefreshToken, userID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create macaroon token")
	}
	return token, nil
}

func (a *Auth) ParseRefreshToken(ctx context.Context, refreshToken string) (*macaroons.Macaroon, *RefreshOnlyCaveat, error) {
	token, err := a.macaroonManager.Parse(ctx, refreshToken)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to parse macaroon token, token: %s", refreshToken)
	}

	if len(token.Caveats) != 1 {
		return nil, nil, errors.Wrap(ErrInvalidRefreshToken, "refresh token must have exactly one caveat")
	}

	roc, ok := token.Caveats[0].(*RefreshOnlyCaveat)
	if !ok {
		return nil, nil, errors.Wrapf(ErrInvalidRefreshToken, "caveat is not a RefreshOnlyCaveat even though it has type %s", CaveatRefreshOnly)
	}

	return token, roc, nil
}

func (a *Auth) InvalidateUserTokens(ctx context.Context, userID int32) error {
	return a.macaroonManager.InvalidateUserTokens(ctx, userID)
}

func (a *Auth) InvalidateToken(ctx context.Context, keyID int64) error {
	return a.macaroonManager.InvalidateToken(ctx, keyID)
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

func GetToken(c *fiber.Ctx) (*macaroons.Macaroon, error) {
	token, ok := c.Locals(ContextKeyMacaroon).(*macaroons.Macaroon)
	if !ok {
		return nil, ErrUserIdentityNotExist
	}
	return token, nil
}
