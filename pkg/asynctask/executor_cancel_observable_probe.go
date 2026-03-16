package asynctask

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
)

func (e *Executor) ExecuteCancelObservableProbe(ctx context.Context, task worker.Task, params *taskgen.CancelObservableProbeParameters) error {
	signalBaseURL := strings.TrimSpace(os.Getenv("CHAOS_SIGNAL_BASE_URL"))
	if params.SignalBaseURL != nil && strings.TrimSpace(*params.SignalBaseURL) != "" {
		signalBaseURL = *params.SignalBaseURL
	}
	signalIntervalMs := int32(200)
	if params.SignalIntervalMs != nil && *params.SignalIntervalMs > 0 {
		signalIntervalMs = *params.SignalIntervalMs
	}
	if err := emitStressProbeSignal(ctx, task.ID, signalBaseURL); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Duration(signalIntervalMs) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return taskInterruptCauseOrErr(ctx)
		case <-ticker.C:
			if err := emitStressProbeSignal(ctx, task.ID, signalBaseURL); err != nil {
				return err
			}
		}
	}
}
