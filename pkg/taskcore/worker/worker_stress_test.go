package worker

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNextNormalClaimGroupsWeightedRotationStress(t *testing.T) {
	w := &Worker{
		weightedLabels:         []string{"w1", "w2"},
		normalClaimWheel:       []string{"w1", "w1", "w2", defaultWeightGroup},
		normalClaimWheelCursor: 0,
	}

	const iterations = 4000
	counts := map[string]int{}

	for i := 0; i < iterations; i++ {
		groups, labels := w.nextNormalClaimGroups()
		require.Equal(t, []string{"w1", "w2"}, labels)
		require.NotEmpty(t, groups)
		require.Contains(t, groups, defaultWeightGroup)

		seen := map[string]struct{}{}
		for _, group := range groups {
			if _, ok := seen[group]; ok {
				t.Fatalf("duplicate group in claim order: %q", group)
			}
			seen[group] = struct{}{}
		}

		counts[groups[0]]++
	}

	quarter := iterations / 4
	require.Equal(t, 2*quarter, counts["w1"])
	require.Equal(t, quarter, counts["w2"])
	require.Equal(t, quarter, counts[defaultWeightGroup])
}

func TestApplyRuntimeConfigLockedClampsAndSanitizesEdgeValues(t *testing.T) {
	w := &Worker{concurrency: 7}

	negative := int32(-3)
	w.applyRuntimeConfigLocked(2, runtimeConfigPayload{
		MaxStrictPercentage: &negative,
		LabelWeights: map[string]int32{
			defaultWeightConfigKey: 0,
			defaultWeightGroup:     2,
			"w1":                   0,
			"w2":                   3,
		},
	})

	require.Equal(t, int64(2), w.runtimeConfigVersion)
	require.Equal(t, int32(0), w.maxStrictPercentage)
	require.Equal(t, 0, w.strictCap)
	require.Equal(t, []string{"w2"}, w.weightedLabels)
	require.Len(t, w.normalClaimWheel, 5)
}

func TestTryReserveStrictInFlightConcurrentStress(t *testing.T) {
	const (
		workers    = 48
		iterations = 500
		strictCap  = 3
	)
	w := &Worker{
		strictCap:      strictCap,
		strictInFlight: 0,
	}

	var (
		wg          sync.WaitGroup
		maxObserved atomic.Int64
		overflowed  atomic.Bool
	)

	readInFlight := func() int {
		w.runtimeMu.Lock()
		defer w.runtimeMu.Unlock()
		return w.strictInFlight
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if !w.tryReserveStrictInFlight() {
					continue
				}

				inFlight := readInFlight()
				if inFlight > strictCap {
					overflowed.Store(true)
				}
				for {
					prev := maxObserved.Load()
					if int64(inFlight) <= prev || maxObserved.CompareAndSwap(prev, int64(inFlight)) {
						break
					}
				}

				time.Sleep(time.Microsecond)
				w.releaseStrictInFlight()
			}
		}()
	}
	wg.Wait()

	require.False(t, overflowed.Load())
	require.LessOrEqual(t, int(maxObserved.Load()), strictCap)
	require.Equal(t, 0, readInFlight())
}
