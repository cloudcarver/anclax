package config

import (
	"os"

	"github.com/cloudcarver/anclax/lib/conf"
	anclax_config "github.com/cloudcarver/anclax/pkg/config"
)

type Config struct {
	Anclax anclax_config.Config `yaml:"anclax,omitempty"`
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
