package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudcarver/anclax/lib/ws"
	"github.com/cloudcarver/anclax/pkg/auth"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"go.uber.org/zap"
)

var log = logger.NewLogAgent("server")

const ContextKeyDisableBodyLog = "anclax_disable_body_log"

func DisableBodyLog(c *fiber.Ctx) {
	c.Locals(ContextKeyDisableBodyLog, true)
}

type Server struct {
	app             *fiber.App
	host            string
	port            int
	auth            auth.AuthInterface
	globalCtx       *globalctx.GlobalContext
	serverInterface apigen.ServerInterface
	validator       apigen.Validator
	wsc             *ws.WebsocketController
	libCfg          *config.LibConfig
	skipLogRequest  func(c *fiber.Ctx) bool
	skipLogResponse func(c *fiber.Ctx) bool
}

func NewServer(
	cfg *config.Config,
	libCfg *config.LibConfig,
	globalCtx *globalctx.GlobalContext,
	auth auth.AuthInterface,
	serverInterface apigen.ServerInterface,
	validator apigen.Validator,
) (*Server, error) {
	// create fiber app
	app := fiber.New(fiber.Config{
		ErrorHandler: utils.ErrorHandler,
		BodyLimit:    50 * 1024 * 1024, // 50MB
	})

	var port = 8020
	if cfg.Port != 0 {
		port = cfg.Port
	} else {
		log.Infof("Using default port: %d", port)
	}

	var host = "localhost"
	if cfg.Host != "" {
		host = cfg.Host
	} else {
		log.Infof("Using default host: %s", host)
	}

	s := &Server{
		app:             app,
		host:            host,
		port:            port,
		auth:            auth,
		serverInterface: serverInterface,
		globalCtx:       globalCtx,
		validator:       validator,
		libCfg:          libCfg,
	}

	s.registerMiddleware()

	middlewares := []apigen.MiddlewareFunc{}
	if cfg.RequestTimeout != nil {
		middlewares = append(
			middlewares,
			func(c *fiber.Ctx) error {
				ctx, cancel := context.WithTimeout(c.Context(), *cfg.RequestTimeout)
				defer cancel()
				c.SetUserContext(ctx)
				return c.Next()
			},
		)
	}

	apigen.RegisterHandlersWithOptions(s.app, apigen.NewXMiddleware(s.serverInterface, s.validator), apigen.FiberServerOptions{
		BaseURL:     "/api/v1",
		Middlewares: middlewares,
	})

	s.skipLogRequest = func(c *fiber.Ctx) bool { return false }
	s.skipLogResponse = func(c *fiber.Ctx) bool { return false }

	if libCfg.Log.RequestPathPrefix != nil && libCfg.Log.HealthCheckPath != nil {
		var (
			prefix     = *libCfg.Log.RequestPathPrefix
			healthPath = *libCfg.Log.HealthCheckPath
		)
		s.skipLogRequest = func(c *fiber.Ctx) bool {
			return !strings.HasPrefix(c.Path(), prefix) || c.Path() == healthPath
		}
		s.skipLogResponse = func(c *fiber.Ctx) bool {
			return !strings.HasPrefix(c.Path(), prefix) || (c.Path() == healthPath && c.Response().StatusCode() < 400)
		}
	} else if libCfg.Log.RequestPathPrefix != nil && libCfg.Log.HealthCheckPath == nil {
		var prefix = *libCfg.Log.RequestPathPrefix
		s.skipLogRequest = func(c *fiber.Ctx) bool {
			return !strings.HasPrefix(c.Path(), prefix)
		}
		s.skipLogResponse = func(c *fiber.Ctx) bool {
			return !strings.HasPrefix(c.Path(), prefix)
		}
	} else if libCfg.Log.RequestPathPrefix == nil && libCfg.Log.HealthCheckPath != nil {
		var healthPath = *libCfg.Log.HealthCheckPath
		s.skipLogRequest = func(c *fiber.Ctx) bool {
			return c.Path() == healthPath
		}
		s.skipLogResponse = func(c *fiber.Ctx) bool {
			return c.Path() == healthPath && c.Response().StatusCode() < 400
		}
	}

	return s, nil
}

func (s *Server) registerMiddleware() {
	s.app.Use(recover.New(recover.Config{
		EnableStackTrace: true,
	}))

	if s.libCfg.Cors != nil {
		s.app.Use(cors.New(*s.libCfg.Cors))
	} else {
		s.app.Use(cors.New(cors.Config{}))
	}

	s.app.Use(requestid.New())
	s.app.Use(func(c *fiber.Ctx) error {
		// log request
		start := time.Now()
		if !s.skipLogRequest(c) {
			log.Info(
				"request",
				zap.String("method", c.Method()),
				zap.String("path", c.Path()),
				zap.String("request-id", c.Locals(requestid.ConfigDefault.ContextKey).(string)),
			)
		}

		err := c.Next()

		// log response
		if !s.skipLogResponse(c) {
			end := time.Now()
			fields := []zap.Field{
				zap.Int("status", c.Response().StatusCode()),
				zap.String("method", c.Method()),
				zap.String("path", c.Path()),
				zap.String("token", fmt.Sprintf("%v", c.Get("Authorization"))),
				zap.String("request-id", c.Locals(requestid.ConfigDefault.ContextKey).(string)),
				zap.Float32("latency-ms", float32(end.Sub(start).Milliseconds())),
				zap.Error(err),
			}
			ct := string(c.Response().Header.ContentType())
			if ct != fiber.MIMEOctetStream && ct != "text/event-stream" && !c.Locals(ContextKeyDisableBodyLog, false).(bool) {
				fields = append(fields, zap.String("body", utils.TruncateString(string(c.Response().Body()), 512)))
			}
			log.Info(
				"response",
				fields...,
			)
		}
		return err
	})
}

func (s *Server) Websocket() *ws.WebsocketController {
	return s.wsc
}

func (s *Server) Listen() error {
	// Create a channel to receive shutdown signal
	shutdownChan := make(chan error)

	// Start the server in a goroutine
	go func() {
		if err := s.app.Listen(fmt.Sprintf(":%d", s.port)); err != nil {
			shutdownChan <- err
		}
	}()

	// Wait for either context cancellation or server error
	select {
	case err := <-shutdownChan:
		return err
	case <-s.globalCtx.Context().Done():
		log.Info("shutting down server due to context cancellation")
		return s.app.Shutdown()
	}
}

func (s *Server) Shutdown() error {
	return s.app.Shutdown()
}

func (s *Server) GetApp() *fiber.App {
	return s.app
}

func (s *Server) GetHost() string {
	return s.host
}

func (s *Server) GetPort() int {
	return s.port
}
