package worker

import (
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPrefetchPacingConsumptionAndRestart(t *testing.T) {
	now := time.Unix(100, 0)
	row := &querier.ListWorkerPrefetchConsumptionRow{ID: uuid.New(), PrefetchClaimed: 9000, Capacity: 100}
	rows := []*querier.ListWorkerPrefetchConsumptionRow{row}
	var p prefetchPacing
	p.observe(now, rows)
	require.Equal(t, 100, p.credit, "bootstrap ignores historical claims")
	n, probe := p.batch(now)
	require.Equal(t, int32(100), n)
	require.False(t, probe)
	p.prepared(now, n, probe)
	row.PrefetchClaimed += 30
	p.observe(now.Add(10*time.Millisecond), rows)
	require.Equal(t, 30, p.credit)
	require.InDelta(t, 3000, p.rate, 0.01)
	require.InDelta(t, float64(15*time.Second/3000), float64(p.interval()), 1)
	p.prepared(now, 12, false)
	require.Equal(t, 18, p.credit, "partial admission preserves unspent demand")
	p.observe(now.Add(20*time.Millisecond), rows)
	require.Equal(t, 18, p.credit, "same sample cannot replenish twice")
	row.PrefetchClaimed = 0
	p.observe(now.Add(30*time.Millisecond), rows)
	require.Equal(t, 18, p.credit, "counter resets do not create negative demand")
	p.observe(now.Add(40*time.Millisecond), nil)
	row.PrefetchClaimed = 5
	p.observe(now.Add(50*time.Millisecond), rows)
	require.Equal(t, 23, p.credit, "absence does not replay the old counter")
	newWorker := &querier.ListWorkerPrefetchConsumptionRow{ID: uuid.New(), PrefetchClaimed: 2, Capacity: 20}
	p.observe(now.Add(60*time.Millisecond), append(rows, newWorker))
	require.Equal(t, 25, p.credit)
	row.PrefetchClaimed = 1000000
	p.observe(now.Add(70*time.Millisecond), rows)
	require.Equal(t, prefetchMaxCredit, p.credit)
}

func TestPrefetchPacingIdleProbeBackoffAndRampUp(t *testing.T) {
	now := time.Unix(100, 0)
	var p prefetchPacing
	p.observe(now, []*querier.ListWorkerPrefetchConsumptionRow{{ID: uuid.New(), Capacity: 100}})
	p.prepared(now, 100, false)
	n, _ := p.batch(now.Add(200 * time.Millisecond))
	require.Zero(t, n, "without consumption skip expensive admission")
	n, probe := p.batch(now.Add(prefetchProbeInterval))
	require.Equal(t, int32(4), n)
	require.True(t, probe)
	p.prepared(now.Add(prefetchProbeInterval), 4, probe)
	require.Equal(t, 96, p.credit, "productive probe restores one startup window")
	p.prepared(now.Add(prefetchProbeInterval), 96, false)
	require.Zero(t, p.credit)
	for _, expected := range []time.Duration{20, 40, 80, 160, 250, 250} {
		p.prepared(now, 0, true)
		require.Equal(t, expected*time.Millisecond, p.interval())
	}
	require.Zero(t, p.credit, "empty probes never mint demand")
	p.prepared(now, 1, true)
	require.Zero(t, p.empty)
}

func TestPrefetchPacingCapacityAndRateBounds(t *testing.T) {
	now := time.Unix(100, 0)
	row := &querier.ListWorkerPrefetchConsumptionRow{ID: uuid.New(), Capacity: 10000}
	var p prefetchPacing
	p.observe(now, []*querier.ListWorkerPrefetchConsumptionRow{row})
	require.Equal(t, prefetchMaxCredit, p.capacity)
	n, _ := p.batch(now)
	require.Equal(t, int32(256), n)
	p.rate = 1000000
	require.Equal(t, prefetchMinInterval, p.interval())
	p.rate = 1
	require.Equal(t, prefetchProbeInterval, p.interval())
	row.Capacity = 1
	p.observe(now.Add(time.Millisecond), []*querier.ListWorkerPrefetchConsumptionRow{row})
	p.rate = 100
	require.Equal(t, 10*time.Millisecond, p.interval(), "small Worker uses a smaller target batch")
}

func TestPrefetchPacingSmallQuotaDoesNotThrottleItself(t *testing.T) {
	now := time.Unix(100, 0)
	row := &querier.ListWorkerPrefetchConsumptionRow{ID: uuid.New(), Capacity: 100}
	var p prefetchPacing
	var available time.Time
	start := now
	// A single finite slot executes for 20 ms. Immediate consumption models
	// an available Worker. Fixed 32/rate pacing falls to four tasks/second.
	for now.Sub(start) < 3*time.Second {
		p.observe(now, []*querier.ListWorkerPrefetchConsumptionRow{row})
		if n, probe := p.batch(now); n > 0 {
			var admitted int32
			if !now.Before(available) {
				admitted = 1
				row.PrefetchClaimed++
				available = now.Add(20 * time.Millisecond)
			}
			p.prepared(now, admitted, probe)
		}
		now = now.Add(p.interval())
	}
	require.GreaterOrEqual(t, row.PrefetchClaimed, int64(60), "pacing must not turn a one-slot resource into a 250 ms poll")
}
