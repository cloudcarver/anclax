package metrics

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestNewMetricsServerUsesSafeDefaults(t *testing.T) {
	m := NewMetricsServer(&config.Config{}, nil)

	require.False(t, m.enable)
	require.Equal(t, "127.0.0.1", m.host)
	require.Equal(t, 9020, m.port)
	require.Equal(t, "127.0.0.1:9020", m.server.Addr)
	m.Start() // A disabled listener needs no global context or socket.
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

func TestMetricsConfigurationTakesPrecedenceOverLegacyPort(t *testing.T) {
	m := NewMetricsServer(&config.Config{
		MetricsPort: 9200,
		Metrics:     config.Metrics{Host: "0.0.0.0", Port: 9300},
	}, nil)
	require.False(t, m.enable)
	require.Equal(t, "0.0.0.0:9300", m.server.Addr)
}

func TestMetricsStartDoesNotReportAnotherServerAsReady(t *testing.T) {
	other := httptest.NewServer(nil)
	t.Cleanup(other.Close)
	host, portString, err := net.SplitHostPort(other.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portString)
	require.NoError(t, err)

	core, observed := observer.New(zapcore.InfoLevel)
	previousLog := log
	log = logger.NewLogAgentWithLogger("metrics", zap.New(core))
	t.Cleanup(func() { log = previousLog })
	globalCtx := globalctx.New()
	t.Cleanup(globalCtx.Cancel)
	m := NewMetricsServer(&config.Config{Metrics: config.Metrics{
		Enable: true,
		Host:   host,
		Port:   port,
	}}, globalCtx)
	m.Start()

	require.Empty(t, observed.FilterLevelExact(zapcore.InfoLevel).All())
	require.Len(t, observed.FilterMessage("metrics server failed to listen").All(), 1)
}

func TestMetricsListenerServesAndStops(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "0.0.0.0", "::1"} {
		t.Run(host, func(t *testing.T) {
			reserved, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
			require.NoError(t, err)
			port := reserved.Addr().(*net.TCPAddr).Port
			require.NoError(t, reserved.Close())
			core, observed := observer.New(zapcore.InfoLevel)
			previousLog := log
			log = logger.NewLogAgentWithLogger("metrics", zap.New(core))
			t.Cleanup(func() { log = previousLog })
			globalCtx := globalctx.New()
			m := NewMetricsServer(&config.Config{Metrics: config.Metrics{
				Enable: true, Host: host, Port: port,
			}}, globalCtx)
			t.Cleanup(func() {
				globalCtx.Cancel()
				require.Eventually(t, func() bool {
					return len(observed.FilterMessage("metrics server shutdown gracefully").All()) == 1
				}, 5*time.Second, time.Millisecond)
			})
			m.Start()
			probeHost := host
			if host == "0.0.0.0" {
				probeHost = "127.0.0.1"
			}
			client := &http.Client{Transport: &http.Transport{}, Timeout: time.Second}
			t.Cleanup(client.CloseIdleConnections)
			resp, err := client.Get("http://" + net.JoinHostPort(probeHost, strconv.Itoa(port)) + "/metrics")
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, resp.Body.Close())
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Contains(t, string(body), "anclax_worker_goroutines")
			require.Empty(t, observed.FilterLevelExact(zapcore.ErrorLevel).All())
		})
	}
}
