package app

import (
	"context"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/logger"
	"go.uber.org/zap"
)

var log = logger.NewLogAgent("debug-server")

type DebugServer struct {
	globalCtx *globalctx.GlobalContext
	host      string
	port      int
	enable    bool
}

func NewDebugServer(cfg *config.Config, globalCtx *globalctx.GlobalContext) *DebugServer {
	host := cfg.Debug.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Debug.Port
	if port == 0 {
		port = 8777
	}
	return &DebugServer{
		globalCtx: globalCtx,
		host:      host,
		port:      port,
		enable:    cfg.Debug.Enable,
	}
}

func (d *DebugServer) Start() error {
	if !d.enable {
		return nil
	}
	server := d.newHTTPServer()

	go func() {
		log.Info("debug server is listening", zap.String("address", server.Addr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("debug server exited", zap.Error(err))
		}
	}()

	<-d.globalCtx.Context().Done()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Error("debug server shutdown error", zap.Error(err))
	} else {
		log.Info("debug server shutdown gracefully")
	}

	return nil
}

func (d *DebugServer) newHTTPServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	return &http.Server{
		Addr:              net.JoinHostPort(d.host, strconv.Itoa(d.port)),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}
}
