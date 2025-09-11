package config

import "github.com/gofiber/fiber/v2/middleware/cors"

type PgCfg struct {
	MaxConnections int32
	MinConnections int32
}

type LibConfig struct {
	Cors *cors.Config
	Pg   *PgCfg
}

func DefaultLibConfig() *LibConfig {
	return &LibConfig{
		Pg: &PgCfg{
			MaxConnections: 10,
			MinConnections: 1,
		},
	}
}
