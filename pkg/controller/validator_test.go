package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudcarver/anclax/pkg/auth"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/hooks"
	"github.com/cloudcarver/anclax/pkg/macaroons"
	macstore "github.com/cloudcarver/anclax/pkg/macaroons/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type identityHandler struct {
	apigen.ServerInterface
	called func(fiber.Ctx) error
}

func (h identityHandler) ListOrgs(c fiber.Ctx) error                { return h.called(c) }
func (h identityHandler) ListTasks(c fiber.Ctx) error               { return h.called(c) }
func (h identityHandler) ListEvents(c fiber.Ctx) error              { return h.called(c) }
func (h identityHandler) SignOut(c fiber.Ctx) error                 { return h.called(c) }
func (h identityHandler) TryExecuteTask(c fiber.Ctx, _ int32) error { return h.called(c) }

func TestGeneratedProtectedRoutesAuthenticateOnce(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/orgs"},
		{http.MethodGet, "/tasks"},
		{http.MethodGet, "/events"},
		{http.MethodPost, "/auth/sign-out"},
		{http.MethodPost, "/tasks/1/try-execute"},
	} {
		for _, credential := range []string{"valid", "missing", "revoked"} {
			t.Run(route.path+"/"+credential, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				parser := macaroons.NewCaveatParser()
				keys := macstore.NewMockKeyStore(ctrl)
				manager := macaroons.NewMacaroonManager(keys, parser)
				authenticator, err := auth.NewAuth(&config.Config{}, manager, parser, hooks.NewMockAnclaxHookInterface(ctrl))
				require.NoError(t, err)
				key := []byte("01234567890123456789012345678901")
				token, err := macaroons.CreateMacaroon(1, key, []macaroons.Caveat{auth.NewUserContextCaveat(7, 9)})
				require.NoError(t, err)
				if credential == "valid" {
					keys.EXPECT().Get(gomock.Any(), int64(1)).Return(key, nil).Times(1)
				} else if credential == "revoked" {
					keys.EXPECT().Get(gomock.Any(), int64(1)).Return(nil, macstore.ErrKeyNotFound).Times(1)
				}
				called := false
				handler := identityHandler{called: func(c fiber.Ctx) error {
					called = true
					user, err := auth.GetUserID(c)
					require.NoError(t, err)
					require.Equal(t, int32(7), user)
					org, err := auth.GetOrgID(c)
					require.NoError(t, err)
					require.Equal(t, int32(9), org)
					return c.SendStatus(http.StatusOK)
				}}
				app := fiber.New()
				apigen.RegisterHandlers(app, apigen.NewXMiddleware(handler, NewValidator(model.NewMockModelInterface(ctrl), authenticator)))
				req := httptest.NewRequest(route.method, route.path, nil)
				if credential != "missing" {
					req.Header.Set("Authorization", "Bearer "+token.StringToken())
				}
				res, err := app.Test(req)
				require.NoError(t, err)
				require.NoError(t, res.Body.Close())
				want := http.StatusUnauthorized
				if credential == "valid" {
					want = http.StatusOK
				}
				require.Equal(t, want, res.StatusCode)
				require.Equal(t, credential == "valid", called)
			})
		}
	}
}
