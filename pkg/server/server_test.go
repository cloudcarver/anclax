package server

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func stringPtr(s string) *string {
	return &s
}

func TestListenAddressUsesConfiguredHost(t *testing.T) {
	tests := []struct {
		name string
		host string
		port int
		want string
	}{
		{name: "loopback", host: "127.0.0.1", port: 8020, want: "127.0.0.1:8020"},
		{name: "hostname", host: "localhost", port: 2910, want: "localhost:2910"},
		{name: "ipv6", host: "::1", port: 8020, want: "[::1]:8020"},
		{name: "explicit wildcard", host: "0.0.0.0", port: 8020, want: "0.0.0.0:8020"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{host: tt.host, port: tt.port}
			require.Equal(t, tt.want, s.listenAddress())
		})
	}
}

func TestListenBindsOnlyTheConfiguredInterface(t *testing.T) {
	reserved, err := net.Listen("tcp", net.JoinHostPort("::1", "0"))
	require.NoError(t, err)
	port := reserved.Addr().(*net.TCPAddr).Port
	require.NoError(t, reserved.Close())

	globalCtx := globalctx.New()
	s, err := NewServer(&config.Config{Host: "::1", Port: port}, config.DefaultLibConfig(), globalCtx, nil, nil, failingAuthValidator{})
	require.NoError(t, err)
	s.GetApp().Get("/listener-test", func(c fiber.Ctx) error {
		return c.SendString("ready")
	})
	ready := make(chan struct{})
	s.GetApp().Hooks().OnListen(func(fiber.ListenData) error {
		close(ready)
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- s.Listen() }()
	t.Cleanup(func() {
		globalCtx.Cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("business listener did not stop after cancellation")
		}
	})
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("business listener did not start")
	}

	client := &http.Client{Transport: &http.Transport{}, Timeout: time.Second}
	t.Cleanup(client.CloseIdleConnections)
	resp, err := client.Get("http://" + net.JoinHostPort("::1", strconv.Itoa(port)) + "/listener-test")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	require.Equal(t, "ready", string(body))

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if conn != nil {
		require.NoError(t, conn.Close())
	}
	require.Error(t, err, "listener unexpectedly accepted traffic on another interface")
}

func TestNewServerDefaultsToLocalhost(t *testing.T) {
	globalCtx := globalctx.New()
	t.Cleanup(globalCtx.Cancel)
	s, err := NewServer(&config.Config{}, config.DefaultLibConfig(), globalCtx, nil, nil, failingAuthValidator{})
	require.NoError(t, err)
	require.Equal(t, "localhost:8020", s.listenAddress())
}

type failingAuthValidator struct {
	apigen.Validator
}

func (failingAuthValidator) AuthFunc(c fiber.Ctx) error {
	if err := c.SendString("response-secret-canary"); err != nil {
		return err
	}
	return errors.New("internal-auth-canary: request-secret-canary")
}

func TestResponseLogsExcludeAuthorizationAndDisabledBodies(t *testing.T) {
	core, observed := observer.New(zapcore.InfoLevel)
	previousLog := log
	log = logger.NewLogAgentWithLogger("server", zap.New(core))
	t.Cleanup(func() { log = previousLog })

	globalCtx := globalctx.New()
	t.Cleanup(globalCtx.Cancel)

	s, err := NewServer(&config.Config{}, config.DefaultLibConfig(), globalCtx, nil, nil, failingAuthValidator{})
	require.NoError(t, err)
	s.GetApp().Get("/credential-response", func(c fiber.Ctx) error {
		DisableBodyLog(c)
		return c.SendString("response-secret-canary")
	})
	s.GetApp().Get("/ordinary-response", func(c fiber.Ctx) error {
		return c.SendString("ordinary response")
	})

	for _, tc := range []struct {
		path   string
		status int
		body   string
	}{
		{"/credential-response", fiber.StatusOK, "response-secret-canary"},
		{"/api/v1/tasks", fiber.StatusUnauthorized, "Unauthorized"},
		{"/ordinary-response", fiber.StatusOK, "ordinary response"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			observed.TakeAll()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer request-secret-canary")
			resp, err := s.GetApp().Test(req)
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, tc.status, resp.StatusCode)
			require.Equal(t, tc.body, string(body))

			entries := observed.FilterMessage("response").All()
			require.Len(t, entries, 1)
			fields, err := json.Marshal(entries[0].ContextMap())
			require.NoError(t, err)
			require.NotContains(t, string(fields), "canary")
			require.NotContains(t, entries[0].ContextMap(), "token")
			if tc.path == "/ordinary-response" {
				require.Equal(t, tc.body, entries[0].ContextMap()["body"])
			}
		})
	}
}

func TestLogRules(t *testing.T) {
	tests := []struct {
		name             string
		logCfg           config.LogCfg
		path             string
		status           int
		wantSkipRequest  bool
		wantSkipResponse bool
	}{
		{
			name:             "no filters logs everything",
			logCfg:           config.LogCfg{},
			path:             "/api/v1/users",
			status:           200,
			wantSkipRequest:  false,
			wantSkipResponse: false,
		},
		{
			name: "request path prefix skips paths outside prefix",
			logCfg: config.LogCfg{
				RequestPathPrefix: stringPtr("/api/v1"),
			},
			path:             "/metrics",
			status:           200,
			wantSkipRequest:  true,
			wantSkipResponse: true,
		},
		{
			name: "request path prefix keeps paths inside prefix",
			logCfg: config.LogCfg{
				RequestPathPrefix: stringPtr("/api/v1"),
			},
			path:             "/api/v1/users",
			status:           200,
			wantSkipRequest:  false,
			wantSkipResponse: false,
		},
		{
			name: "legacy health check path is error only on success",
			logCfg: config.LogCfg{
				HealthCheckPath: stringPtr("/healthz"),
			},
			path:             "/healthz",
			status:           200,
			wantSkipRequest:  true,
			wantSkipResponse: true,
		},
		{
			name: "legacy health check path still logs error responses",
			logCfg: config.LogCfg{
				HealthCheckPath: stringPtr("/healthz"),
			},
			path:             "/healthz",
			status:           500,
			wantSkipRequest:  true,
			wantSkipResponse: false,
		},
		{
			name: "error only path prefixes apply to matching prefix",
			logCfg: config.LogCfg{
				ErrorOnlyPathPrefixes: []string{"/health", "/metrics"},
			},
			path:             "/health/ready",
			status:           200,
			wantSkipRequest:  true,
			wantSkipResponse: true,
		},
		{
			name: "error only path prefixes still log errors",
			logCfg: config.LogCfg{
				ErrorOnlyPathPrefixes: []string{"/health", "/metrics"},
			},
			path:             "/metrics/prometheus",
			status:           503,
			wantSkipRequest:  true,
			wantSkipResponse: false,
		},
		{
			name: "request path prefix and error only prefix both apply",
			logCfg: config.LogCfg{
				RequestPathPrefix:     stringPtr("/api"),
				ErrorOnlyPathPrefixes: []string{"/api/internal/health"},
			},
			path:             "/api/internal/health/live",
			status:           200,
			wantSkipRequest:  true,
			wantSkipResponse: true,
		},
		{
			name: "request path prefix and normal path still log",
			logCfg: config.LogCfg{
				RequestPathPrefix:     stringPtr("/api"),
				ErrorOnlyPathPrefixes: []string{"/api/internal/health"},
			},
			path:             "/api/users",
			status:           200,
			wantSkipRequest:  false,
			wantSkipResponse: false,
		},
		{
			name: "empty error only prefix is ignored",
			logCfg: config.LogCfg{
				ErrorOnlyPathPrefixes: []string{""},
			},
			path:             "/api/users",
			status:           200,
			wantSkipRequest:  false,
			wantSkipResponse: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := newLogRules(tt.logCfg)
			require.Equal(t, tt.wantSkipRequest, rules.shouldSkipRequest(tt.path))
			require.Equal(t, tt.wantSkipResponse, rules.shouldSkipResponse(tt.path, tt.status))
		})
	}
}
