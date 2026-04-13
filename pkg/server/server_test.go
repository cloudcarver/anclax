package server

import (
	"testing"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/stretchr/testify/require"
)

func stringPtr(s string) *string {
	return &s
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
