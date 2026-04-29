package asynctask

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/stretchr/testify/require"
)

func TestExecuteStressProbeNoExplicitSignalsDoesNotEmitWithEnvFallback(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("CHAOS_SIGNAL_BASE_URL", server.URL)

	exec := &Executor{}
	err := exec.ExecuteStressProbe(context.Background(), worker.Task{ID: 11}, &taskgen.StressProbeParameters{
		JobID:   1,
		SleepMs: 60,
		Group:   "default",
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), hits.Load())
}

func TestExecuteStressProbeExplicitSignalIntervalUsesEnvFallback(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/signals/emit" {
			hits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("CHAOS_SIGNAL_BASE_URL", server.URL)
	interval := int32(20)

	exec := &Executor{}
	err := exec.ExecuteStressProbe(context.Background(), worker.Task{ID: 12}, &taskgen.StressProbeParameters{
		JobID:            2,
		SleepMs:          90,
		Group:            "default",
		SignalIntervalMs: &interval,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, hits.Load(), int32(1))
}

func TestExecuteStressProbeExplicitBaseURLUsesDefaultSignalInterval(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/signals/emit" {
			hits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	baseURL := server.URL
	exec := &Executor{}
	err := exec.ExecuteStressProbe(context.Background(), worker.Task{ID: 13}, &taskgen.StressProbeParameters{
		JobID:         3,
		SleepMs:       250,
		Group:         "default",
		SignalBaseURL: &baseURL,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, hits.Load(), int32(1))
}

func TestExecuteStressProbeAlwaysFailModeReturnsRetryableError(t *testing.T) {
	failMode := "always"
	exec := &Executor{}
	err := exec.ExecuteStressProbe(context.Background(), worker.Task{ID: 14}, &taskgen.StressProbeParameters{
		JobID:    4,
		SleepMs:  0,
		Group:    "default",
		FailMode: &failMode,
	})
	require.ErrorContains(t, err, "retryable failure")
}

func TestStressProbeSignalHelpersHandleDisabledTicker(t *testing.T) {
	require.Nil(t, newStressProbeSignalTicker(false, 10))
	require.Nil(t, stressProbeSignalChan(nil))
	stopStressProbeSignalTicker(nil)

	ticker := newStressProbeSignalTicker(true, 1)
	require.NotNil(t, ticker)
	select {
	case <-stressProbeSignalChan(ticker):
	case <-time.After(50 * time.Millisecond):
		t.Fatal("expected ticker event")
	}
	stopStressProbeSignalTicker(ticker)
}
