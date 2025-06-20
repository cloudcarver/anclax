package config

import "github.com/gofiber/fiber/v2/middleware/cors"

type LibConfig struct {
	Cors *cors.Config
}

func DefaultLibConfig() *LibConfig {
	return &LibConfig{}
}
