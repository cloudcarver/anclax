package config

import (
	"github.com/cloudcarver/anclax/lib/ws"
	"github.com/gofiber/fiber/v3/middleware/cors"
)

type PgCfg struct {
	MaxConnections int32
	MinConnections int32
}

type LogCfg struct {
	// (optional) If set, only log entries where the request path starts with this prefix will be logged.
	RequestPathPrefix *string

	// (optional) If set, request logs will be skipped and only error responses will be logged for paths
	// whose request path starts with any of these prefixes.
	ErrorOnlyPathPrefixes []string

	// Deprecated: use ErrorOnlyPathPrefixes.
	// (optional) If set, only error will be logged for this exact health check path.
	HealthCheckPath *string
}

type LibConfig struct {
	Cors *cors.Config
	Pg   *PgCfg
	Log  LogCfg
	Ws   *ws.WsCfg
}

func DefaultLibConfig() *LibConfig {
	return &LibConfig{
		Pg: &PgCfg{
			MaxConnections: 10,
			MinConnections: 1,
		},
	}
}
