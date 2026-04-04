package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudcarver/anclax/pkg/service"
	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

type stubService struct {
	service.ServiceInterface
	signInWithPassword func(context.Context, apigen.SignInRequest) (*apigen.Credentials, error)
}

func (s stubService) SignInWithPassword(ctx context.Context, params apigen.SignInRequest) (*apigen.Credentials, error) {
	return s.signInWithPassword(ctx, params)
}

var _ service.ServiceInterface = stubService{}

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
