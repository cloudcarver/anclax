//go:build smoke
// +build smoke

package e2e_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
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

func TestAuthAPIRefreshTokenConcurrentReplayHasOneWinner(t *testing.T) {
	requireE2E(t)

	ctx := context.Background()
	client := newE2EClient(t)
	credentials := signUp(t, client, fmt.Sprintf("api-race-%d", time.Now().UnixNano()), "test-password")

	const contenders = 12
	start := make(chan struct{})
	type result struct {
		status      int
		credentials *apigen.Credentials
		err         error
	}
	results := make(chan result, contenders)
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			response, err := client.RefreshTokenWithResponse(ctx, apigen.RefreshTokenRequest{
				RefreshToken: credentials.RefreshToken,
			})
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{status: response.StatusCode(), credentials: response.JSON200}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	winners := 0
	losers := 0
	var winner *apigen.Credentials
	for result := range results {
		require.NoError(t, result.err)
		switch result.status {
		case fiber.StatusOK:
			winners++
			winner = result.credentials
		case fiber.StatusUnauthorized:
			losers++
		default:
			t.Fatalf("unexpected refresh status: %d", result.status)
		}
	}
	require.Equal(t, 1, winners)
	require.Equal(t, contenders-1, losers)
	require.NotNil(t, winner)

	valid, err := client.RefreshTokenWithResponse(ctx, apigen.RefreshTokenRequest{
		RefreshToken: winner.RefreshToken,
	})
	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, valid.StatusCode(), string(valid.Body))
}

func TestAuthAPITokensExpireWhileWorkerIsDisabled(t *testing.T) {
	requireE2E(t)

	accessExpiry := 40 * time.Millisecond
	refreshExpiry := 40 * time.Millisecond
	client := newE2EClientWithAuth(t, config.Auth{
		AccessExpiry:  &accessExpiry,
		RefreshExpiry: &refreshExpiry,
	})
	credentials := signUp(t, client, fmt.Sprintf("api-expiry-%d", time.Now().UnixNano()), "test-password")
	time.Sleep(150 * time.Millisecond)

	ctx := context.Background()
	accessResponse, err := client.ListOrgsWithResponse(ctx, bearer(credentials.AccessToken))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusUnauthorized, accessResponse.StatusCode(), string(accessResponse.Body))

	refreshResponse, err := client.RefreshTokenWithResponse(ctx, apigen.RefreshTokenRequest{
		RefreshToken: credentials.RefreshToken,
	})
	require.NoError(t, err)
	require.Equal(t, fiber.StatusUnauthorized, refreshResponse.StatusCode(), string(refreshResponse.Body))
}

func TestAuthAPIConcurrentSignUpCommitsOneUsername(t *testing.T) {
	requireE2E(t)

	ctx := context.Background()
	client := newE2EClient(t)
	username := fmt.Sprintf("api-signup-race-%d", time.Now().UnixNano())

	const contenders = 12
	start := make(chan struct{})
	type result struct {
		status int
		err    error
	}
	results := make(chan result, contenders)
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			response, err := client.SignUpWithResponse(ctx, apigen.SignUpRequest{
				Name:     username,
				Password: "test-password",
			})
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{status: response.StatusCode()}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	created := 0
	conflicts := 0
	for result := range results {
		require.NoError(t, result.err)
		switch result.status {
		case fiber.StatusCreated:
			created++
		case fiber.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected sign-up status: %d", result.status)
		}
	}
	require.Equal(t, 1, created)
	require.Equal(t, contenders-1, conflicts)

	conn, err := pgx.Connect(ctx, e2eDBDSN)
	require.NoError(t, err)
	defer conn.Close(ctx)
	var userCount int
	err = conn.QueryRow(ctx, `SELECT COUNT(*) FROM anclax.users WHERE name = $1`, username).Scan(&userCount)
	require.NoError(t, err)
	require.Equal(t, 1, userCount)
}
