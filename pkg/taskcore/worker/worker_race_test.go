//go:build race
// +build race

package worker

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestWorkerRuntimeStateConcurrentRace(t *testing.T) {
	w := &Worker{concurrency: 32}
	base := int32(40)
	w.runtimeMu.Lock()
	w.applyRuntimeConfigLocked(1, runtimeConfigPayload{
		MaxStrictPercentage: &base,
		LabelWeights: map[string]int32{
			defaultWeightConfigKey: 1,
			"w1":                   3,
			"w2":                   2,
		},
	})
	w.runtimeMu.Unlock()

	var (
		wg                sync.WaitGroup
		invariantViolated atomic.Bool
	)

	writer := func(workerID int) {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			percentage := int32((workerID+i)%140 - 20)
			weights := map[string]int32{
				defaultWeightConfigKey:         int32((i % 3) + 1),
				fmt.Sprintf("w%d", workerID%3): int32((i % 5) + 1),
			}

			w.runtimeMu.Lock()
			w.applyRuntimeConfigLocked(int64((workerID+1)*100000+i), runtimeConfigPayload{
				MaxStrictPercentage: &percentage,
				LabelWeights:        weights,
			})
			w.runtimeMu.Unlock()
		}
	}

	reader := func() {
		defer wg.Done()
		for i := 0; i < 4000; i++ {
			groups, labels := w.nextNormalClaimGroups()
			if len(groups) == 0 {
				invariantViolated.Store(true)
				return
			}

			seen := map[string]struct{}{}
			for _, group := range groups {
				if _, ok := seen[group]; ok {
					invariantViolated.Store(true)
					return
				}
				seen[group] = struct{}{}
			}

			for _, label := range labels {
				if label == defaultWeightGroup {
					invariantViolated.Store(true)
					return
				}
			}

			if w.tryReserveStrictInFlight() {
				w.releaseStrictInFlight()
			}
			if w.currentRuntimeConfigVersion() <= 0 {
				invariantViolated.Store(true)
				return
			}
		}
	}

	for i := 0; i < 6; i++ {
		wg.Add(1)
		go writer(i)
	}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go reader()
	}

	wg.Wait()
	if invariantViolated.Load() {
		t.Fatal("runtime state invariant violated under concurrent access")
	}
}
