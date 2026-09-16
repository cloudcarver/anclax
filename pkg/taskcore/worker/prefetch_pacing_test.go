package worker

import (
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/stretchr/testify/require"
)

func TestPrefetchConsumptionBootstrapPauseAndResume(t *testing.T) {
	var p prefetchPacing
	now := time.Now()
	p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, HasDue: true}})
	batch, paused, _ := p.admission(now)
	require.Equal(t, 256, batch, "empty groups bootstrap without previous consumption")
	require.Empty(t, paused)
	p.prepared(now, map[int64]int64{1: 8})
	batch, paused, _ = p.admission(now)
	require.Zero(t, batch, "a small quota need not reach a fixed 256-task target")
	require.Equal(t, []int64{1}, paused)
	require.Equal(t, 5*time.Millisecond, p.probeInterval(), "discover initial consumption without a slow idle poll")
	p.observe(now.Add(10*time.Millisecond), []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 8, HasDue: true}})
	require.Zero(t, p.groups[1].rate, "our own admissions are not consumption")
	p.observe(now.Add(100*time.Millisecond), []*querier.InspectTaskPrefetchRow{{GroupID: 1, HasDue: true}})
	batch, _, _ = p.admission(now.Add(100 * time.Millisecond))
	require.Positive(t, batch)
	require.Less(t, batch, 256, "measured consumption now sizes the refill")
}

func TestPrefetchConsumptionForecastSchedulesWithoutAnotherProbe(t *testing.T) {
	var p prefetchPacing
	now := time.Now()
	p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 200, HasDue: true}})
	now = now.Add(time.Second)
	p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 100, HasDue: true}})
	batch, _, next := p.admission(now)
	require.Zero(t, batch)
	require.True(t, next.After(now))
	// At 100/s, stock falls below the 250 ms low watermark after 750 ms.
	batch, _, _ = p.admission(now.Add(760 * time.Millisecond))
	require.GreaterOrEqual(t, batch, 26)
	require.InDelta(t, 100, p.groups[1].rate, 0.01, "forecasting must not count as observed consumption")
	p.prepared(now.Add(760*time.Millisecond), map[int64]int64{1: int64(batch)})
	batch, _, _ = p.admission(now.Add(760 * time.Millisecond))
	require.Zero(t, batch, "refilling the full reserve avoids one-task refill spinning")
}

func TestPrefetchConsumptionIndependentGroupsAndNoFixedLimit(t *testing.T) {
	var p prefetchPacing
	now := time.Now()
	p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 100000, HasDue: true}, {GroupID: 2, ReadyCount: 30000, HasDue: true}})
	now = now.Add(100 * time.Millisecond)
	p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 100000, HasDue: true}, {GroupID: 2, ReadyCount: 10000, HasDue: true}, {GroupID: 3, HasDue: true}})
	batch, paused, _ := p.admission(now)
	require.Equal(t, 256, batch)
	require.Equal(t, []int64{1}, paused, "a stocked group cannot suppress a fast or new group")
	p.prepared(now, map[int64]int64{2: 256, 3: 1})
	_, paused, _ = p.admission(now)
	require.ElementsMatch(t, []int64{1, 3}, paused)
}

func TestPrefetchConsumptionIdleInvalidationAndLongPause(t *testing.T) {
	var p prefetchPacing
	now := time.Now()
	p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 500}})
	batch, _, _ := p.admission(now)
	require.Zero(t, batch)
	require.Equal(t, 100*time.Millisecond, p.probeInterval())
	p.observe(now.Add(time.Second), []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 600}})
	require.Zero(t, p.groups[1].rate)
	p.observe(now.Add(2*time.Second), []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 1}})
	require.Equal(t, float64(599), p.groups[1].rate)
	batch, _, _ = p.admission(now.Add(2 * time.Second))
	require.Zero(t, batch, "external invalidation cannot create demand without due work")
	p.observe(now.Add(500*time.Second), []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 10000, HasDue: true}})
	_, _, next := p.admission(now.Add(500 * time.Second))
	require.False(t, next.Before(now.Add(500*time.Second)), "a decayed tiny rate cannot overflow the scheduled duration")
	p.observe(now.Add(501*time.Second), nil)
	require.Empty(t, p.groups)
}

func TestPrefetchEmptyBackoffDoesNotChangeConsumptionOrStopDemand(t *testing.T) {
	var p prefetchPacing
	var b prefetchBackoff
	now := time.Now()
	p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 10, HasDue: true}})
	p.observe(now.Add(time.Second), []*querier.InspectTaskPrefetchRow{{GroupID: 1, HasDue: true}})
	now = now.Add(time.Second)
	for _, ms := range []time.Duration{5, 10, 20, 40, 80, 100, 100} {
		b.result(now, 0)
		require.Equal(t, now.Add(ms*time.Millisecond), b.until)
		batch, _, _ := p.admission(now)
		require.Positive(t, batch, "empty output delays retries but cannot stop demand")
		require.Equal(t, float64(10), p.groups[1].rate)
		now = b.until
	}
	b.result(now, 1)
	require.Zero(t, b.delay)
	require.True(t, b.until.IsZero())
	require.Equal(t, float64(10), p.groups[1].rate)
}

func TestPrefetchLowConsumptionDoesNotRetryForFractionalTask(t *testing.T) {
	var p prefetchPacing
	now := time.Now()
	p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 2, HasDue: true}})
	now = now.Add(time.Second)
	p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 1, HasDue: true}})
	for i := 0; i < 50; i++ {
		batch, _, _ := p.admission(now.Add(50 * time.Millisecond))
		require.Zero(t, batch, "one stocked task covers this rate; fractional demand cannot cause empty retries")
		now = now.Add(100 * time.Millisecond)
		p.observe(now, []*querier.InspectTaskPrefetchRow{{GroupID: 1, ReadyCount: 1, HasDue: true}})
	}
	p.observe(now.Add(100*time.Millisecond), []*querier.InspectTaskPrefetchRow{{GroupID: 1, HasDue: true}})
	batch, _, _ := p.admission(now.Add(100 * time.Millisecond))
	require.Positive(t, batch, "actual depletion still resumes supply")
}
