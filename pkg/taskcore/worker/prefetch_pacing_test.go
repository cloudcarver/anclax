package worker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPrefetchSupplyIdleBackoffAndImmediateRefill(t *testing.T) {
	var p prefetchPacing
	for _, reason := range []string{"idle", "quiescent"} {
		p = prefetchPacing{}
		for _, expected := range []time.Duration{20, 40, 80, 100, 100} {
			interval, err := p.interval(reason)
			require.NoError(t, err)
			require.Equal(t, expected*time.Millisecond, interval)
		}
		interval, err := p.interval("productive")
		require.NoError(t, err)
		require.Zero(t, interval, "new work bypasses the idle backoff without consumption credits")
		require.Zero(t, p.idle)
		interval, err = p.interval("productive")
		require.NoError(t, err)
		require.Zero(t, interval, "successive batches fill the ready window without a rate-imposed sleep")
	}
}

func TestPrefetchSupplyActiveResourcesNeverAccumulateIdleBackoff(t *testing.T) {
	for _, reason := range []string{"ready_full", "blocked"} {
		p := prefetchPacing{idle: prefetchIdleMaximum}
		for i := 0; i < 100; i++ {
			interval, err := p.interval(reason)
			require.NoError(t, err)
			if reason == "ready_full" {
				require.Equal(t, prefetchFullInterval, interval)
			} else {
				require.Equal(t, prefetchBusyInterval, interval)
			}
			require.Zero(t, p.idle, "lack of claims does not mean active leases cannot release resources")
		}
		interval, err := p.interval("idle")
		require.NoError(t, err)
		require.Equal(t, 20*time.Millisecond, interval)
	}
}

func TestPrefetchSupplyRejectsUnknownReason(t *testing.T) {
	var p prefetchPacing
	_, err := p.interval("unexpected")
	require.ErrorContains(t, err, "unknown prefetch wait reason")
}
