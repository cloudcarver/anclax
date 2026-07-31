package app

import (
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestDebugServerUsesSafeManagementDefaults(t *testing.T) {
	d := NewDebugServer(&config.Config{}, nil)
	server := d.newHTTPServer()

	require.False(t, d.enable)
	require.Equal(t, "127.0.0.1", d.host)
	require.Equal(t, 8777, d.port)
	require.Equal(t, "127.0.0.1:8777", server.Addr)
	require.Equal(t, 5*time.Second, server.ReadHeaderTimeout)
	require.Equal(t, 10*time.Second, server.ReadTimeout)
	require.Equal(t, 2*time.Minute, server.WriteTimeout)
	require.Equal(t, 60*time.Second, server.IdleTimeout)
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
