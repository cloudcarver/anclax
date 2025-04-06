package auth

import (
	"context"
	"crypto/ed25519"
	"fmt"
	mathrand "math/rand"
	"time"

	"github.com/cloudcarver/anchor/internal/apigen"
	"github.com/cloudcarver/anchor/internal/config"
	"github.com/cloudcarver/anchor/internal/model"
	"github.com/cloudcarver/anchor/internal/model/querier"
	"github.com/cloudcarver/anchor/internal/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pkg/errors"
)

const UserContextKey = "user"

var ErrUserIdentityNotExist = errors.New("user identity not exists")

type User struct {
	ID             int32
	OrganizationID int32
	AccessRules    map[string]struct{}
}

type AuthStoreInterface interface {
	GetKeys(ctx context.Context) (*apigen.JWKS, error)
	GetKeyByID(ctx context.Context, id string) (*Key, error)
	GetLatestKey(ctx context.Context) (*Key, error)
}

type AuthInterface interface {
	Authfunc(c *fiber.Ctx, rules ...string) error

	// CreateToken creates a new JWT token for the given user ID
	CreateToken(ctx context.Context, user *querier.User, rules []string) (string, error)

	// ValidateToken validates the given token string and returns the user if valid
	ValidateToken(ctx context.Context, tokenString string) (*User, error)

	// CreateRefreshToken returns its JWT token
	CreateRefreshToken(ctx context.Context, userID int32) (string, error)

	// ValidateRefreshToken validates the given signed refresh token and returns the user if valid
	ValidateRefreshToken(ctx context.Context, signedRefreshToken string) (int32, error)

	// GetJWKS returns the JWKS for token validation
	GetJWKS() (*apigen.JWKS, error)
}

type Auth struct {
	authStore          AuthStoreInterface
	refreshTokenExpiry time.Duration
	m                  model.ModelInterface
	now                func() time.Time
}

// Ensure AuthService implements AuthServiceInterface
var _ AuthInterface = (*Auth)(nil)

func NewAuth(cfg *config.Config, store AuthStoreInterface, m model.ModelInterface) (AuthInterface, error) {
	if cfg.Auth.RefreshExpiry == nil {
		return nil, errors.New("refresh token expiry is not set")
	}

	return &Auth{
		authStore:          store,
		refreshTokenExpiry: *cfg.Auth.RefreshExpiry,
		m:                  m,
		now:                time.Now,
	}, nil
}

func (a *Auth) Authfunc(c *fiber.Ctx, rules ...string) error {
	authHeader := c.Get("Authorization")
	if authHeader == "" {
		return c.Status(fiber.StatusUnauthorized).SendString("missing authorization header")
	}

	// Remove "Bearer " prefix if present
	tokenString := authHeader
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		tokenString = authHeader[7:]
	}

	user, err := a.ValidateToken(c.Context(), tokenString)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).SendString(err.Error())
	}

	c.Locals(UserContextKey, user)

	for _, rule := range rules {
		if _, ok := user.AccessRules[rule]; !ok {
			return c.Status(fiber.StatusForbidden).SendString(fmt.Sprintf("Permission denied, need rule %s", rule))
		}
	}
	return nil
}

func (a *Auth) CreateToken(ctx context.Context, user *querier.User, rules []string) (string, error) {
	claims := a.createClaims(user, rules)
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)

	k, err := a.authStore.GetLatestKey(ctx)
	if err != nil {
		return "", err
	}

	token.Header["kid"] = k.ID

	return token.SignedString(k.Priv)
}

func parseToken(tokenString string, pub ed25519.PublicKey) (*jwt.Token, error) {
	return jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return pub, nil
	})
}

func (a *Auth) ValidateToken(ctx context.Context, tokenString string) (*User, error) {
	// Parse the token without validation first to extract the key ID
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %v", err)
	}

	// Get key ID from token header
	kidInterface, ok := token.Header["kid"]
	if !ok {
		return nil, fmt.Errorf("token missing kid in header")
	}

	kid, ok := kidInterface.(string)
	if !ok {
		return nil, fmt.Errorf("invalid kid format in token header")
	}

	// Get the JWK from the store
	k, err := a.authStore.GetKeyByID(ctx, kid)
	if err != nil {
		return nil, fmt.Errorf("failed to get JWK for kid %s: %v", kid, err)
	}

	// Validate the token
	validatedToken, err := parseToken(tokenString, k.Pub)
	if err != nil {
		return nil, err
	}

	claims, ok := validatedToken.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("unexpected error when parsing claims: claims is not jwt.MapClaims")
	}

	exp, ok := claims["exp"].(float64)
	if !ok {
		return nil, fmt.Errorf("failed to parse exp")
	}
	if time.Since(time.Unix(int64(exp), 0)) > 0 {
		return nil, errors.New("token is expired")
	}

	var user User
	if err := utils.JSONConvert(claims["user"], &user); err != nil {
		return nil, errors.Wrapf(err, "failed to parse user from claims: %s", utils.TryMarshal(claims["user"]))
	}
	return &user, nil
}

func (a *Auth) createClaims(user *querier.User, accessRules []string) jwt.MapClaims {
	ruleMap := make(map[string]struct{})
	for _, rule := range accessRules {
		ruleMap[rule] = struct{}{}
	}

	return jwt.MapClaims{
		"user": &User{
			ID:             user.ID,
			OrganizationID: user.OrganizationID,
			AccessRules:    ruleMap,
		},
		"exp": time.Now().Add(12 * time.Hour).Unix(),
	}
}

func (a *Auth) CreateRefreshToken(ctx context.Context, userID int32) (string, error) {
	refreshToken := a.generateOpaqueToken()

	k, err := a.authStore.GetLatestKey(ctx)
	if err != nil {
		return "", err
	}

	// Create JWT with the refresh token
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"userID":       userID,
		"refreshToken": refreshToken,
		"kid":          k.ID,
	})

	signedToken, err := token.SignedString(k.Priv)
	if err != nil {
		return "", err
	}

	err = a.m.StoreRefreshToken(ctx, querier.StoreRefreshTokenParams{
		Token:     refreshToken,
		ExpiredAt: a.now().Add(a.refreshTokenExpiry),
	})
	if err != nil {
		return "", err
	}

	return signedToken, nil
}

func (a *Auth) ValidateRefreshToken(ctx context.Context, signedRefreshToken string) (int32, error) {
	// Parse the token without validation first to extract the key ID
	token, _, err := new(jwt.Parser).ParseUnverified(signedRefreshToken, jwt.MapClaims{})
	if err != nil {
		return 0, fmt.Errorf("failed to parse token: %v", err)
	}

	// Get key ID from token header
	kidInterface, ok := token.Header["kid"]
	if !ok {
		return 0, fmt.Errorf("token missing kid in header")
	}

	kid, ok := kidInterface.(string)
	if !ok {
		return 0, fmt.Errorf("invalid kid format in token header")
	}

	// Get the JWK from the store
	k, err := a.authStore.GetKeyByID(ctx, kid)
	if err != nil {
		return 0, fmt.Errorf("failed to get JWK for kid %s: %v", kid, err)
	}
	// Validate the token
	validatedToken, err := parseToken(signedRefreshToken, k.Pub)
	if err != nil {
		return 0, err
	}

	claims, ok := validatedToken.Claims.(jwt.MapClaims)
	if !ok {
		return 0, errors.New("unexpected error when parsing claims: claims is not jwt.MapClaims")
	}

	userID, ok := claims["userID"].(float64)
	if !ok {
		return 0, errors.Errorf("unexpected error when parsing userID: userID is not float64, got %v", claims["userID"])
	}

	refreshToken, ok := claims["refreshToken"].(string)
	if !ok {
		return 0, errors.Errorf("unexpected error when parsing refreshToken: refreshToken is not string, got %v", claims["refreshToken"])
	}

	rftoken, err := a.m.GetRefreshToken(ctx, refreshToken)
	if err != nil {
		return 0, err
	}

	if rftoken.ExpiredAt.Before(time.Now()) {
		return 0, errors.New("refresh token expired")
	}

	if err := a.m.DeleteRefreshToken(ctx, refreshToken); err != nil {
		return 0, errors.Wrapf(err, "failed to delete refresh token: %s", refreshToken)
	}

	return int32(userID), nil
}

// GetJWKS returns the JWKS (JSON Web Key Set) for the current key
func (a *Auth) GetJWKS() (*apigen.JWKS, error) {
	return a.authStore.GetKeys(context.Background())
}

func (a *Auth) generateOpaqueToken() string {
	currTime := a.now()
	unixMicro := currTime.UnixMicro()

	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+=-*$_[]^&/"
	charsetLen := int64(len(charset))
	b := []byte{}

	for unixMicro > 0 {
		b = append(b, charset[unixMicro&charsetLen])
		unixMicro /= charsetLen
	}

	for i := 0; i < 5; i++ {
		b = append(b, charset[mathrand.Intn(len(charset))])
	}

	return string(b)
}

func GetUser(c *fiber.Ctx) (*User, error) {
	user, ok := c.Locals(UserContextKey).(*User)
	if !ok {
		return nil, ErrUserIdentityNotExist
	}
	return user, nil
}
