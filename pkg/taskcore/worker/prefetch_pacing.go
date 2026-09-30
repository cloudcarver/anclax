package worker

import (
	"math"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/querier"
)

const (
	prefetchBatchSize     = 256
	prefetchReserve       = 500 * time.Millisecond
	prefetchProbeInterval = 50 * time.Millisecond
	prefetchMaintenance   = 250 * time.Millisecond
	prefetchRetryMinimum  = 5 * time.Millisecond
	prefetchIdleMaximum   = 100 * time.Millisecond
)

type prefetchGroupSupply struct {
	ready       int64 // Last observation plus our committed admissions, for departure accounting.
	rate        float64
	due         bool
	available   float64 // Forecast only; never authorizes resource allocation.
	projectedAt time.Time
}

func (g *prefetchGroupSupply) projected(now time.Time) float64 {
	return max(0, g.available-g.rate*max(0, now.Sub(g.projectedAt).Seconds()))
}

// Demand controls normal scheduling. The reserve is measured in consumption
// time, not Worker slots or a fixed minimum queue depth. Empty groups bootstrap
// without consumption, so depleted supply cannot permanently stop admission.
type prefetchPacing struct {
	groups       map[int64]*prefetchGroupSupply
	observed     time.Time
	startupProbe time.Duration
}

func (p *prefetchPacing) observe(now time.Time, rows []*querier.InspectTaskPrefetchRow) {
	elapsed := now.Sub(p.observed).Seconds()
	groups := make(map[int64]*prefetchGroupSupply, len(rows))
	for _, row := range rows {
		g := p.groups[row.GroupID]
		if g == nil {
			g = &prefetchGroupSupply{}
		} else if elapsed > 0 {
			// Cancels/edits also remove ready work and may overestimate demand.
			// Use actual observed departures, never the local forecast, here.
			consumed := max(0, g.ready-row.ReadyCount)
			g.rate = max(float64(consumed)/elapsed, g.rate*math.Exp(-elapsed))
		}
		g.ready, g.due = row.ReadyCount, row.HasDue
		g.available, g.projectedAt = float64(row.ReadyCount), now
		groups[row.GroupID] = g
	}
	p.groups, p.observed = groups, now
	p.startupProbe = min(prefetchIdleMaximum, max(prefetchRetryMinimum, 2*p.startupProbe))
}

func (p *prefetchPacing) prepared(now time.Time, groups map[int64]int64) {
	if p.groups == nil {
		p.groups = make(map[int64]*prefetchGroupSupply)
	}
	for id, count := range groups {
		g := p.groups[id]
		if g == nil {
			g = &prefetchGroupSupply{due: true, projectedAt: now}
			p.groups[id] = g
		}
		g.available, g.projectedAt = g.projected(now)+float64(count), now
		g.ready += count
		if g.rate == 0 && count > 0 {
			p.startupProbe = prefetchRetryMinimum
		}
	}
}

// admission returns current demand and the next forecast threshold crossing.
// A zero batch pauses candidate computation; observations still discover new
// tasks, resource/configuration changes and the first consumption after idle.
func (p *prefetchPacing) admission(now time.Time) (batch int, paused []int64, next time.Time) {
	paused = make([]int64, 0, len(p.groups))
	for id, g := range p.groups {
		stock := g.projected(now)
		target := max(1, g.rate*prefetchReserve.Seconds())
		low := max(1, target/2)
		// A fractional forecast departure must not request a whole extra task
		// when one stocked task already covers a low consumption rate.
		threshold := min(low, target-1)
		if g.due && stock <= threshold {
			need := int(min(float64(prefetchBatchSize), math.Ceil(target-stock)))
			if g.rate == 0 && g.ready == 0 {
				need = prefetchBatchSize // Discovery/bootstrap batch, not a standing stock target.
			}
			batch = min(prefetchBatchSize, batch+max(1, need))
			continue
		}
		paused = append(paused, id)
		if g.due && g.rate > 0 {
			at := now.Add(time.Duration(min(prefetchIdleMaximum.Seconds(), max(0, (stock-threshold)/g.rate))*float64(time.Second)) + time.Millisecond)
			if next.IsZero() || at.Before(next) {
				next = at
			}
		}
	}
	return
}

func (p *prefetchPacing) probeInterval() time.Duration {
	interval := prefetchIdleMaximum
	for _, g := range p.groups {
		if g.due {
			if g.rate > 0 {
				interval = min(interval, prefetchProbeInterval)
			} else if g.ready > 0 {
				interval = min(interval, max(prefetchRetryMinimum, p.startupProbe))
			}
		}
	}
	return interval
}

// Empty results only postpone a retry. They never reset consumption estimates,
// stop demand, or override normal scheduling. Success clears this deadline.
type prefetchBackoff struct {
	delay time.Duration
	until time.Time
}

func (b *prefetchBackoff) result(now time.Time, prepared int32) {
	if prepared > 0 {
		*b = prefetchBackoff{}
		return
	}
	b.delay = min(prefetchIdleMaximum, max(prefetchRetryMinimum, 2*b.delay))
	b.until = now.Add(b.delay)
}
