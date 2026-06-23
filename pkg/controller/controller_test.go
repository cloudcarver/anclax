package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	anclaxauth "github.com/cloudcarver/anclax/pkg/auth"
	"github.com/cloudcarver/anclax/pkg/service"
	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

type stubService struct {
	service.ServiceInterface
	signInWithPassword func(context.Context, apigen.SignInRequest) (*apigen.Credentials, error)
	refreshToken       func(context.Context, string) (*apigen.Credentials, error)
	isUsernameExists   func(context.Context, string) (bool, error)
	createNewUser      func(context.Context, string, string) (*service.UserMeta, error)
	signIn             func(context.Context, int32) (*apigen.Credentials, error)
}

func (s stubService) SignInWithPassword(ctx context.Context, params apigen.SignInRequest) (*apigen.Credentials, error) {
	return s.signInWithPassword(ctx, params)
}

func (s stubService) RefreshToken(ctx context.Context, refreshToken string) (*apigen.Credentials, error) {
	return s.refreshToken(ctx, refreshToken)
}

func (s stubService) IsUsernameExists(ctx context.Context, username string) (bool, error) {
	return s.isUsernameExists(ctx, username)
}

func (s stubService) CreateNewUser(ctx context.Context, username, password string) (*service.UserMeta, error) {
	return s.createNewUser(ctx, username, password)
}

func (s stubService) SignIn(ctx context.Context, userID int32) (*apigen.Credentials, error) {
	return s.signIn(ctx, userID)
}

var _ service.ServiceInterface = stubService{}

type stubAuth struct {
	anclaxauth.AuthInterface
	invalidateUserTokens func(context.Context, int32) error
}

func (s stubAuth) InvalidateUserTokens(ctx context.Context, userID int32) error {
	return s.invalidateUserTokens(ctx, userID)
}

func TestControllerSignInDisabledByDefault(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: utils.ErrorHandler})
	controller := &Controller{
		svc: stubService{
			signInWithPassword: func(context.Context, apigen.SignInRequest) (*apigen.Credentials, error) {
				t.Fatal("sign-in service should not be called when simple auth is disabled")
				return nil, nil
			},
		},
	}
	app.Post("/auth/sign-in", controller.SignIn)

	body, err := json.Marshal(apigen.SignInRequest{Name: "missing-user", Password: "secret"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/auth/sign-in", bytes.NewReader(body))
	req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, fiber.StatusNotFound, resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "Cannot POST /api/v1/auth/sign-in", string(respBody))
}

func TestControllerSignIn(t *testing.T) {
	testCases := []struct {
		name           string
		serviceError   error
		serviceResult  *apigen.Credentials
		expectedStatus int
	}{
		{
			name:           "user not found",
			serviceError:   service.ErrUserNotFound,
			expectedStatus: fiber.StatusNotFound,
		},
		{
			name:           "invalid password",
			serviceError:   service.ErrInvalidPassword,
			expectedStatus: fiber.StatusUnauthorized,
		},
		{
			name: "success",
			serviceResult: &apigen.Credentials{
				AccessToken:  "access-token",
				RefreshToken: "refresh-token",
				TokenType:    apigen.Bearer,
			},
			expectedStatus: fiber.StatusOK,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			app := fiber.New(fiber.Config{ErrorHandler: utils.ErrorHandler})
			controller := &Controller{
				enableSimpleAuth: true,
				svc: stubService{
					signInWithPassword: func(ctx context.Context, params apigen.SignInRequest) (*apigen.Credentials, error) {
						require.Equal(t, apigen.SignInRequest{Name: "missing-user", Password: "secret"}, params)
						return tc.serviceResult, tc.serviceError
					},
				},
			}
			app.Post("/auth/sign-in", controller.SignIn)

			body, err := json.Marshal(apigen.SignInRequest{Name: "missing-user", Password: "secret"})
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/auth/sign-in", bytes.NewReader(body))
			req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, tc.expectedStatus, resp.StatusCode)

			if tc.serviceResult != nil {
				var got apigen.Credentials
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
				require.Equal(t, *tc.serviceResult, got)
			}
		})
	}
}

func TestControllerSignOutInvalidatesUserTokens(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: utils.ErrorHandler})
	userID := int32(201)
	controller := &Controller{
		auth: stubAuth{
			invalidateUserTokens: func(ctx context.Context, gotUserID int32) error {
				require.Equal(t, userID, gotUserID)
				return nil
			},
		},
	}
	app.Post("/auth/sign-out", func(c fiber.Ctx) error {
		c.Locals(anclaxauth.ContextKeyUserID, userID)
		return controller.SignOut(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/auth/sign-out", nil)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, fiber.StatusOK, resp.StatusCode)
}

func TestControllerRefreshToken(t *testing.T) {
	testCases := []struct {
		name           string
		body           string
		serviceError   error
		serviceResult  *apigen.Credentials
		expectedStatus int
		expectService  bool
	}{
		{
			name:           "missing refresh token",
			body:           `{}`,
			expectedStatus: fiber.StatusUnauthorized,
		},
		{
			name:           "invalid json",
			body:           `{`,
			expectedStatus: fiber.StatusBadRequest,
		},
		{
			name:           "expired refresh token",
			body:           `{"refreshToken":"refresh-token"}`,
			serviceError:   service.ErrRefreshTokenExpired,
			expectedStatus: fiber.StatusUnauthorized,
			expectService:  true,
		},
		{
			name: "success",
			body: `{"refreshToken":"refresh-token"}`,
			serviceResult: &apigen.Credentials{
				AccessToken:  "new-access-token",
				RefreshToken: "new-refresh-token",
				TokenType:    apigen.Bearer,
			},
			expectedStatus: fiber.StatusOK,
			expectService:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			app := fiber.New(fiber.Config{ErrorHandler: utils.ErrorHandler})
			controller := &Controller{
				svc: stubService{
					refreshToken: func(ctx context.Context, refreshToken string) (*apigen.Credentials, error) {
						if !tc.expectService {
							t.Fatal("refresh service should not be called")
						}
						require.Equal(t, "refresh-token", refreshToken)
						return tc.serviceResult, tc.serviceError
					},
				},
			}
			app.Post("/auth/refresh", controller.RefreshToken)

			req := httptest.NewRequest(http.MethodPost, "/auth/refresh", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, tc.expectedStatus, resp.StatusCode)

			if tc.serviceResult != nil {
				var got apigen.Credentials
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
				require.Equal(t, *tc.serviceResult, got)
			}
		})
	}
}

func TestControllerSignUpDisabledByDefault(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: utils.ErrorHandler})
	controller := &Controller{
		svc: stubService{
			isUsernameExists: func(context.Context, string) (bool, error) {
				t.Fatal("username lookup should not be called when simple auth is disabled")
				return false, nil
			},
			createNewUser: func(context.Context, string, string) (*service.UserMeta, error) {
				t.Fatal("user creation should not be called when simple auth is disabled")
				return nil, nil
			},
			signIn: func(context.Context, int32) (*apigen.Credentials, error) {
				t.Fatal("sign-in should not be called when simple auth is disabled")
				return nil, nil
			},
		},
	}
	app.Post("/auth/sign-up", controller.SignUp)

	body, err := json.Marshal(apigen.SignUpRequest{Name: "new-user", Password: "secret"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/auth/sign-up", bytes.NewReader(body))
	req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, fiber.StatusNotFound, resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "Cannot POST /api/v1/auth/sign-up", string(respBody))
}
