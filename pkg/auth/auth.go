package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/hooks"
	"github.com/cloudcarver/anclax/pkg/macaroons"
	macaroonstore "github.com/cloudcarver/anclax/pkg/macaroons/store"
	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/gofiber/fiber/v3"
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
	Authfunc(c fiber.Ctx) error

	// CreateTokenWithRefreshToken creates both access token and refresh token
	CreateUserTokens(ctx context.Context, userID int32, orgID int32, caveats ...macaroons.Caveat) (*macaroons.Macaroon, *macaroons.Macaroon, error)

	// CreateToken creates a macaroon token, the group tracks related generated keys.
	CreateToken(ctx context.Context, group string, ttl time.Duration, caveats ...macaroons.Caveat) (*macaroons.Macaroon, error)

	// CreateRefreshToken creates a refresh token for the given group and access token.
	CreateRefreshToken(ctx context.Context, group string, accessToken *macaroons.Macaroon, ttl time.Duration) (*macaroons.Macaroon, error)

	// ParseRefreshToken parses the given refresh token and returns the carrying info
	ParseRefreshToken(ctx context.Context, refreshToken string) (*macaroons.Macaroon, *RefreshOnlyCaveat, error)

	// RotateRefreshToken atomically consumes a refresh token and creates its
	// replacement access/refresh pair. Concurrent calls for one token have at
	// most one winner.
	RotateRefreshToken(ctx context.Context, refreshToken string) (*macaroons.Macaroon, *macaroons.Macaroon, error)

	// InvalidateUserTokens invalidates all tokens for the given user.
	InvalidateUserTokens(ctx context.Context, userID int32) error

	// InvalidateTokensByGroup invalidates all tokens for the given group.
	InvalidateTokensByGroup(ctx context.Context, group string) error

	// InvalidateToken invalidates the token with the given key ID
	InvalidateToken(ctx context.Context, keyID int64) error
}

type Auth struct {
	macaroonManager     macaroons.MacaroonManagerInterface
	caveatParser        macaroons.CaveatParserInterface
	hooks               hooks.AnclaxHookInterface
	timeoutAccessToken  time.Duration
	timeoutRefreshToken time.Duration
}

// Ensure AuthService implements AuthServiceInterface
var _ AuthInterface = (*Auth)(nil)

func UserTokenGroup(userID int32) string {
	return fmt.Sprintf("user:%d", userID)
}

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
		caveatParser:        caveatParser,
		hooks:               hooks,
		timeoutAccessToken:  utils.UnwrapOrDefault(cfg.Auth.AccessExpiry, DefaultTimeoutAccessToken),
		timeoutRefreshToken: utils.UnwrapOrDefault(cfg.Auth.RefreshExpiry, DefaultTimeoutRefreshToken),
	}, nil
}

func (a *Auth) Authfunc(c fiber.Ctx) error {
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
	group := UserTokenGroup(userID)
	accessToken, err := a.macaroonManager.CreateToken(ctx, append(caveats, NewUserContextCaveat(userID, orgID)), a.timeoutAccessToken, group)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create macaroon token")
	}

	refreshToken, err := a.CreateRefreshToken(ctx, group, accessToken, a.timeoutRefreshToken)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create refresh token")
	}

	if err := a.hooks.OnUserTokensCreated(ctx, userID, accessToken); err != nil {
		return nil, nil, errors.Wrap(err, "failed to call hook")
	}

	return accessToken, refreshToken, nil
}

func (a *Auth) CreateToken(ctx context.Context, group string, ttl time.Duration, caveats ...macaroons.Caveat) (*macaroons.Macaroon, error) {
	token, err := a.macaroonManager.CreateToken(ctx, caveats, ttl, group)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create macaroon token")
	}
	return token, nil
}

func (a *Auth) CreateRefreshToken(ctx context.Context, group string, accessToken *macaroons.Macaroon, ttl time.Duration) (*macaroons.Macaroon, error) {
	return a.createRefreshToken(ctx, a.macaroonManager, group, accessToken, ttl)
}

func (a *Auth) createRefreshToken(ctx context.Context, manager macaroons.MacaroonManagerInterface, group string, accessToken *macaroons.Macaroon, ttl time.Duration) (*macaroons.Macaroon, error) {
	if accessToken == nil {
		return nil, errors.New("access token is nil")
	}

	accessCaveats := make([]string, len(accessToken.Caveats))
	for i, caveat := range accessToken.Caveats {
		encoded, err := macaroons.EncodeCaveat(caveat)
		if err != nil {
			return nil, errors.Wrap(err, "failed to encode access token caveat")
		}
		accessCaveats[i] = encoded
	}

	token, err := manager.CreateToken(ctx, []macaroons.Caveat{
		NewRefreshOnlyCaveat(group, accessCaveats),
	}, ttl, group)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create macaroon token")
	}
	return token, nil
}

func (a *Auth) ParseRefreshToken(ctx context.Context, refreshToken string) (*macaroons.Macaroon, *RefreshOnlyCaveat, error) {
	token, err := a.macaroonManager.Parse(ctx, refreshToken)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to parse macaroon token")
	}
	roc, err := a.parseRefreshMacaroon(token)
	return token, roc, err
}

func (a *Auth) parseRefreshMacaroon(token *macaroons.Macaroon) (*RefreshOnlyCaveat, error) {
	if token == nil {
		return nil, errors.Wrap(ErrInvalidRefreshToken, "refresh token is nil")
	}
	if len(token.Caveats) != 1 {
		return nil, errors.Wrap(ErrInvalidRefreshToken, "refresh token must have exactly one caveat")
	}

	roc, ok := token.Caveats[0].(*RefreshOnlyCaveat)
	if !ok {
		return nil, errors.Wrapf(ErrInvalidRefreshToken, "caveat is not a RefreshOnlyCaveat even though it has type %s", CaveatRefreshOnly)
	}

	parsedCaveats := make([]macaroons.Caveat, len(roc.AccessCaveats))
	for i, encoded := range roc.AccessCaveats {
		caveat, err := a.caveatParser.Parse(encoded)
		if err != nil {
			return nil, errors.Wrap(ErrInvalidRefreshToken, "failed to parse access token caveat")
		}
		parsedCaveats[i] = caveat
	}
	roc.AccessTokenCaveats = parsedCaveats

	return roc, nil
}

func (a *Auth) RotateRefreshToken(ctx context.Context, refreshToken string) (*macaroons.Macaroon, *macaroons.Macaroon, error) {
	var accessToken, newRefreshToken *macaroons.Macaroon
	err := a.macaroonManager.RunTransaction(ctx, func(manager macaroons.MacaroonManagerInterface) error {
		consumed, err := manager.Consume(ctx, refreshToken)
		if err != nil {
			if isRejectedTokenError(err) {
				return fmt.Errorf("%w: failed to consume refresh token: %w", ErrInvalidRefreshToken, err)
			}
			return errors.Wrap(err, "failed to consume refresh token")
		}

		roc, err := a.parseRefreshMacaroon(consumed)
		if err != nil {
			return err
		}

		if roc.Group != "" {
			if err := manager.InvalidateTokensByGroup(ctx, roc.Group); err != nil {
				return errors.Wrap(err, "failed to invalidate token group")
			}
		}

		accessToken, err = manager.CreateToken(ctx, roc.AccessTokenCaveats, a.timeoutAccessToken, roc.Group)
		if err != nil {
			return errors.Wrap(err, "failed to create access token")
		}

		newRefreshToken, err = a.createRefreshToken(ctx, manager, roc.Group, accessToken, a.timeoutRefreshToken)
		if err != nil {
			return errors.Wrap(err, "failed to create refresh token")
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return accessToken, newRefreshToken, nil
}

func isRejectedTokenError(err error) bool {
	return errors.Is(err, macaroons.ErrMalformedToken) ||
		errors.Is(err, macaroons.ErrInvalidSignature) ||
		errors.Is(err, macaroonstore.ErrKeyNotFound)
}

func (a *Auth) InvalidateUserTokens(ctx context.Context, userID int32) error {
	return a.InvalidateTokensByGroup(ctx, UserTokenGroup(userID))
}

func (a *Auth) InvalidateTokensByGroup(ctx context.Context, group string) error {
	return a.macaroonManager.InvalidateTokensByGroup(ctx, group)
}

func (a *Auth) InvalidateToken(ctx context.Context, keyID int64) error {
	return a.macaroonManager.InvalidateToken(ctx, keyID)
}

func GetUserID(c fiber.Ctx) (int32, error) {
	userID, ok := c.Locals(ContextKeyUserID).(int32)
	if !ok {
		return 0, ErrUserIdentityNotExist
	}
	return userID, nil
}

func GetOrgID(c fiber.Ctx) (int32, error) {
	orgID, ok := c.Locals(ContextKeyOrgID).(int32)
	if !ok {
		return 0, ErrUserIdentityNotExist
	}
	return orgID, nil
}

func GetToken(c fiber.Ctx) (*macaroons.Macaroon, error) {
	token, ok := c.Locals(ContextKeyMacaroon).(*macaroons.Macaroon)
	if !ok {
		return nil, ErrUserIdentityNotExist
	}
	return token, nil
}
