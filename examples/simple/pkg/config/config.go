package config

import (
	"os"

	"github.com/cloudcarver/anchor/lib/conf"
	anchor_config "github.com/cloudcarver/anchor/pkg/config"
)

type Config struct {
	Anchor anchor_config.Config `yaml:"anchor,omitempty"`
}

const (
	envPrefix  = "MYAPP_"
	configFile = "app.yaml"
)

func NewConfig() (*Config, error) {
	c := &Config{}
	if err := conf.FetchConfig((func() string {
		if _, err := os.Stat(configFile); err != nil {
			return ""
		}
		return configFile
	})(), envPrefix, c); err != nil {
		return nil, err
	}

	return c, nil
}
