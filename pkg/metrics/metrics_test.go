package metrics

import (
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestNewMetricsServerUsesSafeDefaults(t *testing.T) {
	m := NewMetricsServer(&config.Config{}, nil)

	require.False(t, m.enable)
	require.Equal(t, "127.0.0.1", m.host)
	require.Equal(t, 9020, m.port)
	require.Equal(t, "127.0.0.1:9020", m.server.Addr)
	require.Equal(t, 5*time.Second, m.server.ReadHeaderTimeout)
	require.Equal(t, 10*time.Second, m.server.ReadTimeout)
	require.Equal(t, 30*time.Second, m.server.WriteTimeout)
	require.Equal(t, 60*time.Second, m.server.IdleTimeout)
}

func TestNewMetricsServerAllowsExplicitManagementAddress(t *testing.T) {
	m := NewMetricsServer(&config.Config{Metrics: config.Metrics{
		Enable: true,
		Host:   "::1",
		Port:   9100,
	}}, nil)

	require.True(t, m.enable)
	require.Equal(t, "[::1]:9100", m.server.Addr)
}

func TestLegacyMetricsPortRemainsAnExplicitOptIn(t *testing.T) {
	m := NewMetricsServer(&config.Config{MetricsPort: 9200}, nil)

	require.True(t, m.enable)
	require.Equal(t, "127.0.0.1:9200", m.server.Addr)
}

func TestMetricsProbeHostUsesLoopbackForWildcardListeners(t *testing.T) {
	require.Equal(t, "127.0.0.1", metricsProbeHost("0.0.0.0"))
	require.Equal(t, "::1", metricsProbeHost("::"))
	require.Equal(t, "metrics.internal", metricsProbeHost("metrics.internal"))
}
