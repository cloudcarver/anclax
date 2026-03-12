package asynctask

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
)

func (e *Executor) ExecuteStressProbe(ctx context.Context, task worker.Task, params *taskgen.StressProbeParameters) error {
	sleep := time.Duration(params.SleepMs) * time.Millisecond
	signalBaseURL := strings.TrimSpace(os.Getenv("CHAOS_SIGNAL_BASE_URL"))
	if params.SignalBaseURL != nil && strings.TrimSpace(*params.SignalBaseURL) != "" {
		signalBaseURL = *params.SignalBaseURL
	}
	signalIntervalMs := int32(0)
	if params.SignalIntervalMs != nil {
		signalIntervalMs = *params.SignalIntervalMs
	}
	if sleep <= 0 {
		if err := emitStressProbeSignal(ctx, task.ID, signalBaseURL); err != nil {
			return err
		}
		return taskInterruptCauseOrErr(ctx)
	}

	timer := time.NewTimer(sleep)
	defer timer.Stop()

	signalTicker := newStressProbeSignalTicker(signalIntervalMs)
	defer signalTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return taskInterruptCauseOrErr(ctx)
		case <-timer.C:
			return nil
		case <-signalTicker.C:
			if err := emitStressProbeSignal(ctx, task.ID, signalBaseURL); err != nil {
				return err
			}
		}
	}
}

func newStressProbeSignalTicker(intervalMs int32) *time.Ticker {
	if intervalMs <= 0 {
		intervalMs = 200
	}
	return time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
}

func emitStressProbeSignal(ctx context.Context, taskID int32, baseURL string) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if taskID <= 0 || baseURL == "" {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"taskID": taskID})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/signals/emit", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return taskInterruptCauseOrErr(ctx)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("stress probe signal emit status=%s", resp.Status)
	}
	return nil
}

func taskInterruptCauseOrErr(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}
