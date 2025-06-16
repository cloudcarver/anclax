package config

import (
	"os"

	"github.com/cloudcarver/anchor/lib/conf"
	anchor_config "github.com/cloudcarver/anchor/pkg/config"
)

type Pg struct {
	// (Required) The DSN (Data Source Name) for postgres database connection. If specified, Host, Port, User, Password, and Db settings will be ignored.
	DSN *string `yaml:"dsn,omitempty"`
}

type Config struct {
	// (Optional) The host of the server, default is localhost
	Host string `yaml:"host,omitempty"`

	// The postgres configuration
	Pg Pg `yaml:"pg,omitempty"`

	Anchor anchor_config.Config `yaml:"anchor,omitempty"`
}

const (
	envPrefix  = "ACP_"
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
