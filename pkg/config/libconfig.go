package config

import "github.com/gofiber/fiber/v2/middleware/cors"

type PgCfg struct {
	MaxConnections int32
	MinConnections int32
}

type LogCfg struct {
	// (optional) If set, only log entries where the request path starts with this prefix will be logged.
	RequestPathPrefix *string

	// (optional) If set, only error will be logged for the health check path.
	HealthCheckPath *string
}

type WsCfg struct {
	// (optional) Default is false, set to true to enable websocket support.
	EnableWebsocket bool

	// (optional) Default is /ws, the path to accept websocket connections.
	WebSocketPath string

	// (optional) Default is 1MB, the maximum size of a message that can be read from the websocket connection.
	ReadLimit int64

	// (optional) Default is 40 seconds, the idle timeout for the websocket connection.
	IdleTimeoutSeconds int64

	// (optional) Default is 30 seconds, the interval to send ping messages to the client.
	PingIntervalSeconds int64

	// (optional) Default is 10 seconds, the time to wait for a write to complete.
	WriteWaitSeconds int64

	// (optional) Default is ws_session_id, the key to store the session ID in the websocket connection locals.
	SessionIDKey string
}

type LibConfig struct {
	Cors *cors.Config
	Pg   *PgCfg
	Log  LogCfg
	Ws   *WsCfg
}

func DefaultLibConfig() *LibConfig {
	return &LibConfig{
		Pg: &PgCfg{
			MaxConnections: 10,
			MinConnections: 1,
		},
	}
}
