package conf

import (
	"fmt"
	"os"
	"path/filepath"
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

func TestConfigErrorsDoNotDiscloseInvalidValues(t *testing.T) {
	const secret = "LEAKME"
	t.Run("typed YAML value", func(t *testing.T) {
		err := marshallRawYAML([]byte("port: "+secret+"\n"), &secretTestConfig{})
		require.Error(t, err)
		require.NotContains(t, err.Error(), secret)
	})
	t.Run("typed environment value", func(t *testing.T) {
		t.Setenv("SAFE_CONFIG_PORT", secret)
		err := FetchConfig("", "SAFE_CONFIG_", &secretTestConfig{})
		require.Error(t, err)
		require.NotContains(t, err.Error(), secret)
	})
	t.Run("explicit YAML tag", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte("password: !!int "+secret+"\n"), 0600))
		err := FetchConfig(path, "SAFE_CONFIG_", &secretTestConfig{})
		require.Error(t, err)
		require.NotContains(t, err.Error(), secret)
	})
	t.Run("environment map mismatch", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte("pg:\n  host: localhost\n"), 0600))
		t.Setenv("SAFE_CONFIG_PG", secret)
		err := FetchConfig(path, "SAFE_CONFIG_", &secretTestConfig{})
		require.Error(t, err)
		require.NotContains(t, err.Error(), secret)
	})
}

func TestFetchConfigPreservesEnvironmentPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("password: file-value\nport: 5432\n"), 0600))
	t.Setenv("SAFE_CONFIG_PASSWORD", "environment-value")
	var cfg secretTestConfig
	require.NoError(t, FetchConfig(path, "SAFE_CONFIG_", &cfg))
	require.Equal(t, "environment-value", cfg.Password)
	require.Equal(t, 5432, cfg.Port)
}
