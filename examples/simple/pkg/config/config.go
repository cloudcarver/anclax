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

type Debug struct {
	// (Optional) Whether to enable the debug server, default is false
	Enable bool `yaml:"enable,omitempty"`

	// (Optional) The port of the debug server, default is 8080
	Port int `yaml:"port,omitempty"`
}

type Config struct {
	Debug Debug `yaml:"debug,omitempty"`

	// (Optional) The host of the server, default is localhost
	Host string `yaml:"host,omitempty"`

	// The postgres configuration
	Pg Pg `yaml:"pg,omitempty"`
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

func NewAnchorConfig() (*anchor_config.Config, error) {
	anchorCfg := &anchor_config.Config{}
	if err := conf.FetchConfig((func() string {
		if _, err := os.Stat(configFile); err != nil {
			return ""
		}
		return configFile
	})(), envPrefix, anchorCfg); err != nil {
		return nil, err
	}

	return anchorCfg, nil
}
