package worker

import (
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/stretchr/testify/require"
)

func TestPrefetchSupplyPauseAndResume(t *testing.T) {
	var p prefetchPacing
	now := time.Now()
	p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, HasDue: true}})
	wanted, paused := p.admission()
	require.True(t, wanted, "zero consumption bootstraps an empty group")
	require.Empty(t, paused)
	p.prepared(map[int64]int64{1: 256})
	wanted, paused = p.admission()
	require.False(t, wanted, "candidate SQL stops when reserve is sufficient")
	require.Equal(t, []int64{1}, paused)
	p.observe(now.Add(100*time.Millisecond), []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 256, HasDue: true}})
	require.Zero(t, p.groups[1].rate, "our own admissions are not consumption")
	wanted, _ = p.admission()
	require.False(t, wanted)
	p.observe(now.Add(200*time.Millisecond), []*querier.InspectTaskPrefetchRow{{GroupID: 1, HasDue: true}})
	wanted, _ = p.admission()
	require.True(t, wanted, "draining the reserve immediately permits refill")
	require.InDelta(t, 2560, p.groups[1].rate, 0.01)
	p.prepared(map[int64]int64{1: 256})
	wanted, _ = p.admission()
	require.True(t, wanted, "productive batches continue toward the time reserve")
}

func TestPrefetchSupplyNoFixedQueueLimitAndGroupIsolation(t *testing.T) {
	var p prefetchPacing
	now := time.Now()
	p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 100000, HasDue: true}, {GroupID: 2, ReadyCount: 30000, HasDue: true}})
	p.observe(now.Add(100*time.Millisecond), []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 100000, HasDue: true}, {GroupID: 2, ReadyCount: 10000, HasDue: true}, {GroupID: 3, HasDue: true}})
	wanted, paused := p.admission()
	require.True(t, wanted)
	require.Equal(t, []int64{1}, paused, "a slow stocked group must not suppress a fast or new group")
	require.True(t, p.groups[2].needsSupply(), "even more than 4096 ready can need supply")
	p.prepared(map[int64]int64{2: 256, 3: 1})
	require.True(t, p.groups[3].needsSupply(), "a one-slot quota cannot become a demand-credit bottleneck")
}

func TestPrefetchSupplyIdleAndExplicitInvalidation(t *testing.T) {
	var p prefetchPacing
	now := time.Now()
	p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 500}})
	wanted, _ := p.admission()
	require.False(t, wanted)
	require.Equal(t, 100*time.Millisecond, p.probeInterval())
	// External additions/configuration changes do not create negative demand.
	p.observe(now.Add(time.Second), []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 600}})
	require.Zero(t, p.groups[1].rate)
	// Cancel/edit departures may overestimate demand, but no due work means no admission.
	p.observe(now.Add(2*time.Second), []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 1}})
	require.Equal(t, float64(599), p.groups[1].rate)
	wanted, _ = p.admission()
	require.False(t, wanted)
	p.observe(now.Add(3*time.Second), nil)
	require.Empty(t, p.groups)
}

func TestPrefetchSupplyRetryWithoutRateSleep(t *testing.T) {
	var p prefetchPacing
	for _, reason := range []string{"idle", "quiescent"} {
		p.idle = 0
		for _, expected := range []time.Duration{20, 40, 80, 100, 100} {
			interval, err := p.interval(reason)
			require.NoError(t, err)
			require.Equal(t, expected*time.Millisecond, interval)
		}
	}
	interval, err := p.interval("productive")
	require.NoError(t, err)
	require.Zero(t, interval)
	for i := 0; i < 100; i++ {
		interval, err = p.interval("blocked")
		require.NoError(t, err)
		require.Equal(t, 5*time.Millisecond, interval)
	}
	_, err = p.interval("unexpected")
	require.Error(t, err)
}
