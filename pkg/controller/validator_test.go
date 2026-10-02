package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudcarver/anclax/pkg/auth"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type authenticatedOrgsHandler struct {
	apigen.ServerInterface
}

func (authenticatedOrgsHandler) ListOrgs(c fiber.Ctx) error {
	userID, err := auth.GetUserID(c)
	if err != nil {
		return err
	}
	return c.JSON(userID)
}

func TestDefaultValidatorAuthenticatesProtectedRequestOnce(t *testing.T) {
	for _, authorized := range []bool{true, false} {
		t.Run(fmt.Sprintf("authorized=%t", authorized), func(t *testing.T) {
			a := auth.NewMockAuthInterface(gomock.NewController(t))
			a.EXPECT().Authfunc(gomock.Any()).DoAndReturn(func(c fiber.Ctx) error {
				if !authorized {
					return fiber.ErrUnauthorized
				}
				return auth.NewUserContextCaveat(7, 9).Validate(c)
			}).Times(1)
			app := fiber.New()
			middleware := apigen.NewXMiddleware(authenticatedOrgsHandler{}, NewValidator(nil, a))
			app.Get("/orgs", middleware.ListOrgs)
			resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/orgs", nil))
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			wantStatus := http.StatusUnauthorized
			if authorized {
				wantStatus = http.StatusOK
			}
			require.Equal(t, wantStatus, resp.StatusCode)
		})
	}
}
