//go:build smoke
// +build smoke

package e2e_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

func TestAuthAPIExistingFlow(t *testing.T) {
	requireE2E(t)

	ctx := context.Background()
	client := newE2EClient(t)
	username := fmt.Sprintf("api-user-%d", time.Now().UnixNano())
	password := "test-password"

	signUpResp, err := client.SignUpWithResponse(ctx, apigen.SignUpRequest{
		Name:     username,
		Password: password,
	})
	require.NoError(t, err)
	require.Equal(t, fiber.StatusCreated, signUpResp.StatusCode(), string(signUpResp.Body))
	require.NotNil(t, signUpResp.JSON201)
	credentials := *signUpResp.JSON201
	require.NotEmpty(t, credentials.AccessToken)
	require.NotEmpty(t, credentials.RefreshToken)
	require.Equal(t, apigen.Bearer, credentials.TokenType)

	duplicate, err := client.SignUpWithResponse(ctx, apigen.SignUpRequest{
		Name:     username,
		Password: password,
	})
	require.NoError(t, err)
	require.Equal(t, fiber.StatusConflict, duplicate.StatusCode(), string(duplicate.Body))

	unauthorizedOrgs, err := client.ListOrgsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusUnauthorized, unauthorizedOrgs.StatusCode(), string(unauthorizedOrgs.Body))

	orgs, err := client.ListOrgsWithResponse(ctx, bearer(credentials.AccessToken))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, orgs.StatusCode(), string(orgs.Body))
	require.NotNil(t, orgs.JSON200)
	require.Len(t, *orgs.JSON200, 1)

	refreshed, err := client.RefreshTokenWithResponse(ctx, apigen.RefreshTokenRequest{
		RefreshToken: credentials.RefreshToken,
	})
	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, refreshed.StatusCode(), string(refreshed.Body))
	require.NotNil(t, refreshed.JSON200)
	newCredentials := *refreshed.JSON200
	require.NotEqual(t, credentials.AccessToken, newCredentials.AccessToken)
	require.NotEqual(t, credentials.RefreshToken, newCredentials.RefreshToken)

	oldAccess, err := client.ListOrgsWithResponse(ctx, bearer(credentials.AccessToken))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusUnauthorized, oldAccess.StatusCode(), string(oldAccess.Body))

	newAccess, err := client.ListOrgsWithResponse(ctx, bearer(newCredentials.AccessToken))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, newAccess.StatusCode(), string(newAccess.Body))

	oldRefresh, err := client.RefreshTokenWithResponse(ctx, apigen.RefreshTokenRequest{
		RefreshToken: credentials.RefreshToken,
	})
	require.NoError(t, err)
	require.Equal(t, fiber.StatusUnauthorized, oldRefresh.StatusCode(), string(oldRefresh.Body))

	otherCredentials := signUp(t, client, fmt.Sprintf("api-other-%d", time.Now().UnixNano()), password)

	signOut, err := client.SignOutWithResponse(ctx, bearer(newCredentials.AccessToken))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, signOut.StatusCode(), string(signOut.Body))

	signedOutAccess, err := client.ListOrgsWithResponse(ctx, bearer(newCredentials.AccessToken))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusUnauthorized, signedOutAccess.StatusCode(), string(signedOutAccess.Body))

	otherAccess, err := client.ListOrgsWithResponse(ctx, bearer(otherCredentials.AccessToken))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, otherAccess.StatusCode(), string(otherAccess.Body))
}
