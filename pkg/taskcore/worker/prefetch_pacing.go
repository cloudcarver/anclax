package worker

import (
	"fmt"
	"time"
)

const (
	prefetchBusyInterval = 5 * time.Millisecond
	prefetchIdleMaximum  = 100 * time.Millisecond
)

// Supply is bounded by the database ready window, not by historical demand.
// Only genuine idle/quiescent outcomes accumulate backoff. An active resource
// owner can finish and release permits without advancing any claim counter.
type prefetchPacing struct {
	idle time.Duration
}

func (p *prefetchPacing) interval(reason string) (time.Duration, error) {
	switch reason {
	case "productive":
		p.idle = 0
		return 0, nil
	case "ready_full", "blocked":
		p.idle = 0
		return prefetchBusyInterval, nil
	case "idle", "quiescent":
		p.idle = min(prefetchIdleMaximum, max(20*time.Millisecond, 2*p.idle))
		return p.idle, nil
	default:
		return 0, fmt.Errorf("unknown prefetch wait reason %q", reason)
	}
}
