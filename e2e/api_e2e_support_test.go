//go:build smoke
// +build smoke

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/app"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	anclaxwire "github.com/cloudcarver/anclax/wire"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

const (
	composeProject = "anclax_e2e"
	composeFile    = "docker-compose.yaml"
	e2eDBDSN       = "postgres://postgres:postgres@127.0.0.1:7432/postgres?sslmode=disable"
	e2eBaseURL     = "http://anclax.test/api/v1"
)

var e2eSkipReason string

func TestMain(m *testing.M) {
	if !dockerAvailable() {
		e2eSkipReason = "docker not available"
		os.Exit(m.Run())
	}

	_ = compose("down", "--remove-orphans")
	if err := compose("up", "-d", "db"); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start e2e compose project: %v\n", err)
		_ = compose("down", "--remove-orphans")
		os.Exit(1)
	}

	if err := waitForPostgres(e2eDBDSN, 30*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "postgres not ready: %v\n", err)
		_ = compose("down", "--remove-orphans")
		os.Exit(1)
	}

	code := m.Run()
	if err := compose("down", "--remove-orphans"); err != nil {
		fmt.Fprintf(os.Stderr, "failed to stop e2e compose project: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

type fiberHTTPClient struct {
	app *fiber.App
}

func (c fiberHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.app.Test(req)
}

func requireE2E(t *testing.T) {
	t.Helper()
	if e2eSkipReason != "" {
		t.Skip(e2eSkipReason)
	}
}

func newE2EClient(t *testing.T) *apigen.ClientWithResponses {
	t.Helper()

	anclaxApp := newE2EApplication(t)
	client, err := apigen.NewClientWithResponses(
		e2eBaseURL,
		apigen.WithHTTPClient(fiberHTTPClient{app: anclaxApp.GetServer().GetApp()}),
	)
	require.NoError(t, err)
	return client
}

func newE2EApplication(t *testing.T) *app.Application {
	t.Helper()

	dsn := e2eDBDSN
	cfg := &config.Config{
		EnableSimpleAuth: true,
		Pg: config.Pg{
			DSN: &dsn,
		},
		Worker: config.Worker{
			Disable: true,
		},
	}
	libCfg := config.DefaultLibConfig()

	anclaxApp, err := anclaxwire.InitializeApplication(cfg, libCfg)
	require.NoError(t, err)
	t.Cleanup(anclaxApp.Close)
	return anclaxApp
}

func signUp(t *testing.T, client *apigen.ClientWithResponses, username, password string) apigen.Credentials {
	t.Helper()

	resp, err := client.SignUpWithResponse(context.Background(), apigen.SignUpRequest{
		Name:     username,
		Password: password,
	})
	require.NoError(t, err)
	require.Equal(t, fiber.StatusCreated, resp.StatusCode(), string(resp.Body))
	require.NotNil(t, resp.JSON201)
	return *resp.JSON201
}

func bearer(token string) apigen.RequestEditorFn {
	return func(ctx context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

func dockerAvailable() bool {
	cmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	return cmd.Run() == nil
}

func compose(args ...string) error {
	fullArgs := append([]string{"compose", "-p", composeProject, "-f", composeFile}, args...)
	cmd := exec.Command("docker", fullArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %v failed: %w: %s", fullArgs, err, string(output))
	}
	return nil
}

func waitForPostgres(dsn string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			_ = conn.Close(ctx)
			cancel()
			return nil
		}
		cancel()
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for postgres")
}
