package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var TaskSchedulerDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name: "anclax_task_scheduler_duration_seconds", Help: "Scheduling operation latency, including pool wait and retries.",
	Buckets: prometheus.DefBuckets,
}, []string{"operation"})

var TaskSchedulerErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "anclax_task_scheduler_errors_total", Help: "Scheduling operation errors by SQLSTATE (other for non-PostgreSQL errors).",
}, []string{"operation", "sqlstate"})

var TaskClaimBatchSize = promauto.NewHistogram(prometheus.HistogramOpts{
	Name: "anclax_task_claim_batch_size", Help: "Tasks returned by each automatic batch claim, including empty claims.",
	Buckets: []float64{0, 1, 8, 16, 32, 64, 128, 256},
})

var TaskFinalizeRetriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "anclax_task_finalize_retries_total", Help: "Retries of explicitly rolled-back finalization transactions.",
}, []string{"sqlstate"})

var WorkerTaskPhases = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "anclax_worker_task_phases", Help: "Reserved/admitted task slots by phase, including the control lane.",
}, []string{"phase"})
