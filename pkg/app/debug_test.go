package app

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDebugServerUsesSafeManagementDefaults(t *testing.T) {
	d := NewDebugServer(&config.Config{}, nil)
	server := d.newHTTPServer()

	require.False(t, d.enable)
	require.Equal(t, "127.0.0.1", d.host)
	require.Equal(t, 8777, d.port)
	require.Equal(t, "127.0.0.1:8777", server.Addr)
	require.NoError(t, d.Start())
}

func TestDebugListenerServesAndStops(t *testing.T) {
	for _, host := range []string{"", "::1"} {
		t.Run("host="+host, func(t *testing.T) {
			probeHost := host
			if probeHost == "" {
				probeHost = "127.0.0.1"
			}
			reserved, err := net.Listen("tcp", net.JoinHostPort(probeHost, "0"))
			require.NoError(t, err)
			port := reserved.Addr().(*net.TCPAddr).Port
			require.NoError(t, reserved.Close())
			globalCtx := globalctx.New()
			d := NewDebugServer(&config.Config{Debug: config.Debug{
				Enable: true, Host: host, Port: port,
			}}, globalCtx)
			done := make(chan error, 1)
			go func() { done <- d.Start() }()
			t.Cleanup(func() {
				globalCtx.Cancel()
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-time.After(5 * time.Second):
					t.Error("debug listener did not stop after cancellation")
				}
			})
			client := &http.Client{Transport: &http.Transport{}, Timeout: time.Second}
			t.Cleanup(client.CloseIdleConnections)
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				resp, err := client.Get("http://" + net.JoinHostPort(probeHost, strconv.Itoa(port)) + "/debug/pprof/")
				if !assert.NoError(c, err) {
					return
				}
				body, err := io.ReadAll(resp.Body)
				assert.NoError(c, resp.Body.Close())
				assert.NoError(c, err)
				assert.Equal(c, http.StatusOK, resp.StatusCode)
				assert.Contains(c, string(body), "Types of profiles available")
			}, 5*time.Second, 10*time.Millisecond)
		})
	}
}

func TestDebugServerAllowsExplicitManagementAddress(t *testing.T) {
	d := NewDebugServer(&config.Config{Debug: config.Debug{
		Enable: true,
		Host:   "::1",
		Port:   9777,
	}}, nil)

	require.True(t, d.enable)
	require.Equal(t, "[::1]:9777", d.newHTTPServer().Addr)
}
