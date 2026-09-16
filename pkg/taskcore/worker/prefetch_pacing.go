package worker

import (
	"fmt"
	"math"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/querier"
)

const (
	prefetchBatchSize     = 256
	prefetchReserve       = 500 * time.Millisecond
	prefetchProbeInterval = 50 * time.Millisecond
	prefetchMaintenance   = 250 * time.Millisecond
	prefetchBusyInterval  = 5 * time.Millisecond
	prefetchIdleMaximum   = 100 * time.Millisecond
)

type prefetchGroupSupply struct {
	ready int64
	rate  float64
	due   bool
}

// The target is a time reserve, never an admission or queue capacity limit.
// A bootstrap batch and immediate refill of an empty group avoid feeding a
// supply shortage back into a lower demand estimate. Each group is independent.
type prefetchPacing struct {
	groups   map[int64]*prefetchGroupSupply
	observed time.Time
	idle     time.Duration
}

func (p *prefetchPacing) observe(now time.Time, rows []*querier.InspectTaskPrefetchRow) {
	elapsed := now.Sub(p.observed).Seconds()
	groups := make(map[int64]*prefetchGroupSupply, len(rows))
	for _, row := range rows {
		g := p.groups[row.GroupID]
		if g == nil {
			g = &prefetchGroupSupply{}
		} else if elapsed > 0 {
			// ready includes our own committed admissions since the last probe.
			// Cancels/edits can also remove ready work: treating those departures
			// as consumption only overestimates reserve, never grants resources.
			consumed := max(0, g.ready-row.ReadyCount)
			g.rate = max(float64(consumed)/elapsed, g.rate*math.Exp(-elapsed))
		}
		g.ready, g.due = row.ReadyCount, row.HasDue
		groups[row.GroupID] = g
	}
	p.groups, p.observed = groups, now
}

func (p *prefetchPacing) prepared(groups map[int64]int64) {
	if p.groups == nil {
		p.groups = make(map[int64]*prefetchGroupSupply)
	}
	for id, count := range groups {
		g := p.groups[id]
		if g == nil {
			g = &prefetchGroupSupply{due: true}
			p.groups[id] = g
		}
		g.ready += count
	}
}

func (g *prefetchGroupSupply) needsSupply() bool {
	return g.due && float64(g.ready) < max(float64(prefetchBatchSize), g.rate*prefetchReserve.Seconds())
}

func (p *prefetchPacing) admission() (bool, []int64) {
	paused := make([]int64, 0, len(p.groups))
	wanted := false
	for id, g := range p.groups {
		if g.needsSupply() {
			wanted = true
		} else {
			paused = append(paused, id)
		}
	}
	return wanted, paused
}

func (p *prefetchPacing) probeInterval() time.Duration {
	for _, g := range p.groups {
		if g.due {
			return prefetchProbeInterval
		}
	}
	return prefetchIdleMaximum
}

func (p *prefetchPacing) interval(reason string) (time.Duration, error) {
	switch reason {
	case "productive":
		p.idle = 0
		return 0, nil
	case "blocked":
		p.idle = 0
		return prefetchBusyInterval, nil
	case "idle", "quiescent":
		p.idle = min(prefetchIdleMaximum, max(20*time.Millisecond, 2*p.idle))
		return p.idle, nil
	default:
		return 0, fmt.Errorf("unknown prefetch wait reason %q", reason)
	}
}
