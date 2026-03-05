package taskcoredtmtest_test

import (
	"context"
	"testing"

	taskcoredtmtest "github.com/cloudcarver/anclax/pkg/taskcore/dtmtest/gen"
	"github.com/stretchr/testify/require"
)

func TestDSTDeterministicRuntimeScenarios(t *testing.T) {
	h := newRuntimeHarness()
	t.Cleanup(func() {
		_ = h.Stop(context.Background())
	})

	err := taskcoredtmtest.RunAll(context.Background(), func(ctx context.Context) (taskcoredtmtest.Actors, error) {
		return taskcoredtmtest.Actors{
			Runtime: h,
		}, nil
	})
	require.NoError(t, err)
}
