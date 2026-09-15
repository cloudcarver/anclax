package worker

import (
	"math"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
)

const (
	prefetchProbeInterval = 250 * time.Millisecond
	prefetchMinInterval   = 5 * time.Millisecond
	prefetchInitialWait   = 20 * time.Millisecond
	prefetchMaxCredit     = 4096
)

// Counters are hints for admission volume, not permits. The database still
// fences the scheduler, reserves resources, and enforces the ready capacity.
type prefetchPacing struct {
	counters map[uuid.UUID]int64
	sampled  time.Time
	lastRun  time.Time
	capacity int
	credit   int
	rate     float64
	burst    float64
	empty    time.Duration
}

func (p *prefetchPacing) observe(now time.Time, rows []*querier.ListWorkerPrefetchConsumptionRow) {
	initial := p.counters == nil
	if initial {
		p.counters = make(map[uuid.UUID]int64)
	}
	var consumed int64
	p.capacity = 0
	for _, row := range rows {
		previous, seen := p.counters[row.ID]
		// Ignore history on scheduler restart. A new Worker starts its counter
		// at zero; a reset or temporarily absent Worker must not replay demand.
		if !initial && row.PrefetchClaimed >= previous && (seen || row.Capacity > 0) {
			consumed += row.PrefetchClaimed - previous
		}
		p.counters[row.ID] = row.PrefetchClaimed
		p.capacity = min(prefetchMaxCredit, p.capacity+int(row.Capacity))
	}
	if initial {
		p.credit = p.capacity
	} else {
		p.credit = int(min(int64(prefetchMaxCredit), int64(p.credit)+consumed))
		if consumed > 0 {
			if p.burst == 0 {
				p.burst = float64(consumed)
			} else {
				p.burst += 0.25 * (float64(consumed) - p.burst)
			}
		}
		if elapsed := now.Sub(p.sampled).Seconds(); elapsed > 0 {
			instant := float64(consumed) / elapsed
			if p.rate == 0 && consumed > 0 {
				p.rate = instant
			} else {
				alpha := 1 - math.Exp(-elapsed/0.2)
				p.rate += alpha * (instant - p.rate)
			}
		}
	}
	p.sampled = now
}

func (p *prefetchPacing) batch(now time.Time) (size int32, probe bool) {
	if p.credit > 0 {
		return int32(min(256, p.credit)), false
	}
	if p.lastRun.IsZero() || now.Sub(p.lastRun) >= prefetchProbeInterval {
		return int32(min(4, max(1, p.capacity))), true
	}
	return 0, false
}

func (p *prefetchPacing) prepared(started time.Time, n int32, probe bool) {
	p.lastRun = started
	if n == 0 {
		p.empty = min(prefetchProbeInterval, max(prefetchInitialWait, p.empty*2))
		return
	}
	p.empty = 0
	p.credit = max(0, p.credit-int(n))
	if probe {
		// One successful idle probe starts a new window. Without this, a queue
		// with no recent consumption would be limited to four jobs per probe.
		p.credit = max(p.credit, p.capacity-int(n))
	}
}

// Intervals are start-to-start; expensive SQL must not add another full sleep.
func (p *prefetchPacing) interval() time.Duration {
	if p.empty > 0 {
		return p.empty
	}
	if p.rate < 1 {
		return prefetchInitialWait
	}
	batch := float64(min(32, max(1, p.capacity)))
	if p.burst > 0 {
		// A fixed target of 32 would throttle a one-slot tag to the maximum
		// interval. Aim below the observed consumption burst so pacing can
		// accelerate after a slowdown instead of reproducing its own delay.
		batch = min(batch, max(0.5, p.burst/2))
	}
	return max(prefetchMinInterval, min(prefetchProbeInterval,
		time.Duration(float64(time.Second)*batch/p.rate)))
}
