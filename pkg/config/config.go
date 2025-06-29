package config

import (
	"time"
)

type Pg struct {
	// (Required) The DSN (Data Source Name) for postgres database connection. If specified, Host, Port, User, Password, and Db settings will be ignored.
	DSN *string `yaml:"dsn"`
}

type Auth struct {
	AccessExpiry  *time.Duration `yaml:"accessexp"`
	RefreshExpiry *time.Duration `yaml:"refreshexp"`
}

type TestAccount struct {
	// The password of the test account, if not set, there will be no test account
	Password string `yaml:"password"`
}

type Worker struct {
	// (Optional) Whether to disable the worker, default is false
	Disable bool `yaml:"disable"`
}

type Debug struct {
	// (Optional) Whether to enable the debug server, default is false
	Enable bool `yaml:"enable"`

	// (Optional) The port of the debug server, default is 8080
	Port int `yaml:"port"`
}

type Config struct {
	// (Optional) The path of file to store the initialization data, if not set, skip the initialization
	Init string `yaml:"init"`

	// (Optional) The host of the anchor server, it is used in the API endpoint of the web UI.
	// If not set, the host will be localhost.
	Host string `yaml:"host"`

	// (Optional) The port of the anchor server, default is 8020
	Port int `yaml:"port"`

	// The Auth configuration
	Auth Auth `yaml:"auth"`

	// The postgres configuration
	Pg Pg `yaml:"pg"`

	// The test account configuration
	TestAccount *TestAccount `yaml:"testaccount"`

	// (Optional) The port of the metrics server, default is 9020
	MetricsPort int `yaml:"metricsport"`

	Worker Worker `yaml:"worker"`

	Debug Debug `yaml:"debug"`

	// (Optional) The timeout for the request, default is no timeout
	RequestTimeout *time.Duration `yaml:"requesttimeout"`
}
