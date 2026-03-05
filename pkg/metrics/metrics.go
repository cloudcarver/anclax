package metrics

import (
	"context"
	"fmt"
	"net/http"
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

type MetricsServer struct {
	port      int
	server    *http.Server
	globalCtx *globalctx.GlobalContext
}

func (m *MetricsServer) Start() {
	go func() {
		log.Infof("metrics server is listening on port %d", m.port)
		if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server exited", zap.Error(err))
		}
	}()

	// Shutdown the server when the global context is done
	go func() {
		<-m.globalCtx.Context().Done()
		if err := m.server.Shutdown(context.Background()); err != nil {
			log.Error("metrics server shutdown error", zap.Error(err))
		} else {
			log.Info("metrics server shutdown gracefully")
		}
	}()

	ready := make(chan struct{})

	go func() {
		for range 5 {
			resp, err := http.Get(fmt.Sprintf("http://localhost:%d/metrics", m.port))
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
	port := 9020
	if cfg.MetricsPort != 0 {
		port = cfg.MetricsPort
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	return &MetricsServer{
		port:      port,
		server:    server,
		globalCtx: globalCtx,
	}
}
