package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var TaskLeaseRenewalDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "anclax_task_lease_renewal_duration_seconds",
	Help:    "Time spent renewing a task lease batch, including connection acquisition.",
	Buckets: prometheus.DefBuckets,
})

var TaskLeaseRenewalBatchSize = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "anclax_task_lease_renewal_batch_size",
	Help:    "Number of task attempts in a renewal query.",
	Buckets: []float64{1, 8, 32, 64, 128, 256},
})

var TaskLeaseRenewalBatchesTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "anclax_task_lease_renewal_batches_total", Help: "Total task lease renewal queries.",
})

var TaskLeaseRenewalErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "anclax_task_lease_renewal_errors_total", Help: "Task lease renewal queries that failed.",
})

var TaskLeaseRenewalLostTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "anclax_task_lease_renewal_lost_total", Help: "Attempts interrupted after lease expiry or rejected renewal.",
})
