package conf

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type secretTestConfig struct {
	Password string `yaml:"password"`
	Port     int    `yaml:"port"`
}

func TestMarshallRawYAMLDoesNotReturnMergedConfiguration(t *testing.T) {
	raw := []byte("password: raw-secret-canary\nport: not-an-integer\n")
	err := marshallRawYAML(raw, &secretTestConfig{})

	require.Error(t, err)
	require.NotContains(t, err.Error(), "raw-secret-canary")
	require.NotContains(t, err.Error(), fmt.Sprint(raw))
}

func TestFetchConfigDoesNotReturnEnvironmentSecrets(t *testing.T) {
	t.Setenv("SAFE_CONFIG_PASSWORD", "environment-secret-canary")
	t.Setenv("SAFE_CONFIG_PORT", "not-an-integer")

	err := FetchConfig("", "SAFE_CONFIG_", &secretTestConfig{})

	require.Error(t, err)
	require.NotContains(t, err.Error(), "environment-secret-canary")
	require.Contains(t, err.Error(), "failed to unmarshal yaml config")
}
