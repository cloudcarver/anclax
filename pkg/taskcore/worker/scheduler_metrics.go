package worker

import (
	"errors"
	"time"

	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/jackc/pgx/v5/pgconn"
)

func observeScheduler(operation string, start time.Time, err error) {
	metrics.TaskSchedulerDurationSeconds.WithLabelValues(operation).Observe(time.Since(start).Seconds())
	if err != nil {
		code := "other"
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			code = pgErr.Code
		}
		metrics.TaskSchedulerErrorsTotal.WithLabelValues(operation, code).Inc()
	}
}
