package metrics

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var log = logger.NewLogAgent("metrics")

var WorkerGoroutines = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "anclax_worker_goroutines",
		Help: "The number of goroutines that are running",
	},
)

var PulledTasks = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "anclax_pulled_tasks",
		Help: "The number of tasks that have been pulled",
	},
)

var RunTaskErrors = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "anclax_run_task_internal_errors",
		Help: "The number of internal errors during running tasks, not related to the task logic. This is expected to be 0.",
	},
)

var WorkerStrictInFlight = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "anclax_worker_strict_inflight",
		Help: "Current number of strict-priority tasks in flight for this worker process.",
	},
)

var WorkerStrictCap = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "anclax_worker_strict_cap",
		Help: "Current strict-priority concurrency cap for this worker process.",
	},
)

var WorkerStrictSaturationTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "anclax_worker_strict_saturation_total",
		Help: "Total number of strict-claim attempts rejected because strict in-flight reached strict cap.",
	},
)

var WorkerRuntimeConfigVersion = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "anclax_worker_runtime_config_version",
		Help: "Applied runtime config version for this worker process.",
	},
)

var RuntimeConfigLaggingWorkers = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "anclax_runtime_config_lagging_workers",
		Help: "Current count of alive workers lagging behind a runtime config target version.",
	},
)

var RuntimeConfigConvergenceSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "anclax_runtime_config_convergence_seconds",
		Help:    "Time taken for a runtime config update task to converge on all alive workers.",
		Buckets: prometheus.DefBuckets,
	},
)

var RuntimeConfigSupersededTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "anclax_runtime_config_superseded_total",
		Help: "Total number of runtime config update tasks that exited because a newer config version superseded them.",
	},
)

var TaskListenerPollDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "anclax_task_listener_poll_duration_seconds",
		Help:    "Time spent querying a batch of task statuses in the polling task listener.",
		Buckets: prometheus.DefBuckets,
	},
)

type MetricsServer struct {
	enable    bool
	host      string
	port      int
	server    *http.Server
	globalCtx *globalctx.GlobalContext
}

func (m *MetricsServer) Start() {
	if !m.enable {
		return
	}

	go func() {
		log.Infof("metrics server is listening on %s", m.server.Addr)
		if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server exited", zap.Error(err))
		}
	}()

	// Shutdown the server when the global context is done
	go func() {
		<-m.globalCtx.Context().Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.server.Shutdown(ctx); err != nil {
			log.Error("metrics server shutdown error", zap.Error(err))
		} else {
			log.Info("metrics server shutdown gracefully")
		}
	}()

	ready := make(chan struct{})

	go func() {
		client := &http.Client{Timeout: time.Second}
		probeURL := url.URL{
			Scheme: "http",
			Host:   net.JoinHostPort(metricsProbeHost(m.host), strconv.Itoa(m.port)),
			Path:   "/metrics",
		}
		for range 5 {
			resp, err := client.Get(probeURL.String())
			if err == nil {
				resp.Body.Close()
				close(ready)
				return
			}
			time.Sleep(time.Second)
		}
	}()

	// Wait for the server to be ready or timeout
	select {
	case <-ready:
		log.Info("metrics server started successfully")
	case <-time.After(5 * time.Second):
		panic("timed out waiting for metrics server to start")
	}
}

func NewMetricsServer(cfg *config.Config, globalCtx *globalctx.GlobalContext) *MetricsServer {
	enable := cfg.Metrics.Enable
	host := cfg.Metrics.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := 9020
	if cfg.Metrics.Port != 0 {
		port = cfg.Metrics.Port
	} else if cfg.MetricsPort != 0 {
		port = cfg.MetricsPort
		enable = true
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:              net.JoinHostPort(host, strconv.Itoa(port)),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return &MetricsServer{
		enable:    enable,
		host:      host,
		port:      port,
		server:    server,
		globalCtx: globalCtx,
	}
}

func metricsProbeHost(host string) string {
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsUnspecified() {
		return host
	}
	if ip.To4() != nil {
		return "127.0.0.1"
	}
	return "::1"
}
