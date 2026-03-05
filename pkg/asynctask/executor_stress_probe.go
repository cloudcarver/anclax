package asynctask

import (
	"context"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
)

func (e *Executor) ExecuteStressProbe(ctx context.Context, params *taskgen.StressProbeParameters) error {
	sleep := time.Duration(params.SleepMs) * time.Millisecond
	if sleep <= 0 {
		return nil
	}
	timer := time.NewTimer(sleep)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

