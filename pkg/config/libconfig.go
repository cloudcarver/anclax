package config

import "github.com/gofiber/fiber/v2/middleware/cors"

type PgCfg struct {
	MaxConnections int32
	MinConnections int32
}

type LogCfg struct {
	// (optional) If set, only log entries where the request path starts with this prefix will be logged.
	RequestPathPrefix *string

	// (optional) If set, only error will be logged for the health check path.
	HealthCheckPath *string
}

type LibConfig struct {
	Cors *cors.Config
	Pg   *PgCfg
	Log  LogCfg
}

func DefaultLibConfig() *LibConfig {
	return &LibConfig{
		Pg: &PgCfg{
			MaxConnections: 10,
			MinConnections: 1,
		},
	}
}
