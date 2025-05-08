package config

import (
	"os"

	"github.com/cloudcarver/anchor/pkg/conf"
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

	// The postgres configuration
	Pg Pg `yaml:"pg,omitempty"`
}

func NewConfig() (*Config, error) {
	c := &Config{}
	if err := conf.FetchConfig((func() string {
		if _, err := os.Stat("app.yaml"); err != nil {
			return ""
		}
		return "app.yaml"
	})(), "ACP_", c); err != nil {
		return nil, err
	}
	return c, nil
}
