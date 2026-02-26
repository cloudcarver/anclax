package taskcoredtmtest_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaimRaceNeverExceedsConcurrencyGo(t *testing.T) {
	h := newRuntimeHarness()
	t.Cleanup(func() { _ = h.Stop(context.Background()) })

	require.NoError(t, h.Start(context.Background(), 3, 100))
	require.NoError(t, h.SetExecuteBlocking(context.Background(), true))
	for i := 1; i <= 6; i++ {
		require.NoError(t, h.QueueStrictTask(context.Background(), fmt.Sprintf("R%d", i), int32(10-i)))
	}
	for i := 0; i < 12; i++ {
		require.NoError(t, h.Poll(context.Background()))
	}

	require.NoError(t, h.WaitCallCount(context.Background(), "claim_strict", 3, 3000))
	require.NoError(t, h.WaitCallCount(context.Background(), "execute", 3, 3000))
	require.NoError(t, h.WaitSnapshot(context.Background(), 3, 3, 3, 3000))
	require.NoError(t, h.AssertInvariants(context.Background()))

	require.NoError(t, h.ReleaseExecutions(context.Background(), 3))
	require.NoError(t, h.WaitCallCount(context.Background(), "finalize", 3, 3000))
	require.NoError(t, h.WaitSnapshot(context.Background(), 0, 0, 0, 3000))
	require.NoError(t, h.AssertInvariants(context.Background()))

	for i := 0; i < 12; i++ {
		require.NoError(t, h.Poll(context.Background()))
	}
	require.NoError(t, h.WaitCallCount(context.Background(), "claim_strict", 6, 3000))
	require.NoError(t, h.WaitCallCount(context.Background(), "execute", 6, 3000))
	require.NoError(t, h.ReleaseExecutions(context.Background(), 3))
	require.NoError(t, h.WaitCallCount(context.Background(), "finalize", 6, 3000))
	require.NoError(t, h.WaitSnapshot(context.Background(), 0, 0, 0, 3000))
	require.NoError(t, h.AssertInvariants(context.Background()))
}

func TestStrictCapBoundaryMatrixGo(t *testing.T) {
	h := newRuntimeHarness()
	t.Cleanup(func() { _ = h.Stop(context.Background()) })

	require.NoError(t, h.Start(context.Background(), 7, 100))
	cases := []struct {
		req     string
		version int32
		pct     int32
		cap     int32
	}{
		{"req-cap-0", 1, 0, 0},
		{"req-cap-1", 2, 1, 1},
		{"req-cap-24", 3, 24, 2},
		{"req-cap-25", 4, 25, 2},
		{"req-cap-26", 5, 26, 2},
		{"req-cap-50", 6, 50, 4},
		{"req-cap-99", 7, 99, 7},
		{"req-cap-100", 8, 100, 7},
	}

	for _, c := range cases {
		require.NoError(t, h.SetRefreshConfig(context.Background(), c.req, c.version, c.pct, 1, 0, 0))
		require.NoError(t, h.NotifyRuntimeConfig(context.Background(), c.req))
		require.NoError(t, h.WaitCallCount(context.Background(), "ack_config", c.version, 3000))
		require.NoError(t, h.AssertAck(context.Background(), c.req, c.version))
		require.NoError(t, h.AssertStrictCap(context.Background(), c.cap))
		require.NoError(t, h.AssertInvariants(context.Background()))
	}
}

func TestNormalWheelFairnessDeterminismGo(t *testing.T) {
	h := newRuntimeHarness()
	t.Cleanup(func() { _ = h.Stop(context.Background()) })

	require.NoError(t, h.Start(context.Background(), 1, 0))
	require.NoError(t, h.SetRefreshConfig(context.Background(), "req-wheel", 5, 0, 1, 2, 1))
	require.NoError(t, h.NotifyRuntimeConfig(context.Background(), "req-wheel"))
	require.NoError(t, h.WaitCallCount(context.Background(), "refresh_config", 1, 3000))
	require.NoError(t, h.WaitCallCount(context.Background(), "ack_config", 1, 3000))

	for i := 0; i < 20; i++ {
		require.NoError(t, h.QueueNormalNoTask(context.Background()))
	}
	for i := 0; i < 4; i++ {
		require.NoError(t, h.Poll(context.Background()))
		require.NoError(t, h.WaitSnapshot(context.Background(), 0, 0, 0, 3000))
	}

	require.NoError(t, h.WaitCallCount(context.Background(), "claim_normal", 12, 3000))
	require.NoError(t, h.AssertClaimGroupsPrefix(context.Background(), []string{"__default__", "w1", "w2", "w1", "w2", "__default__", "w1", "w2", "__default__", "w2", "__default__", "w1"}))
	require.NoError(t, h.AssertInvariants(context.Background()))
}

func TestErrorPathMatrixGo(t *testing.T) {
	h := newRuntimeHarness()
	t.Cleanup(func() { _ = h.Stop(context.Background()) })

	require.NoError(t, h.Start(context.Background(), 1, 100))
	require.NoError(t, h.ClearRuntimeErrors(context.Background()))
	require.NoError(t, h.ClearPortErrors(context.Background()))

	require.NoError(t, h.SetPortError(context.Background(), "claim_strict", "", true))
	require.NoError(t, h.Poll(context.Background()))
	require.NoError(t, h.WaitRuntimeErrorCount(context.Background(), 1, 3000))
	require.NoError(t, h.SetPortError(context.Background(), "claim_strict", "", false))

	require.NoError(t, h.QueueStrictTask(context.Background(), "E_EXEC", 9))
	require.NoError(t, h.SetPortError(context.Background(), "execute", "E_EXEC", true))
	require.NoError(t, h.Poll(context.Background()))
	require.NoError(t, h.WaitCallCount(context.Background(), "execute", 1, 3000))
	require.NoError(t, h.WaitCallCount(context.Background(), "finalize", 1, 3000))
	require.NoError(t, h.WaitRuntimeErrorCount(context.Background(), 1, 3000))
	require.NoError(t, h.SetPortError(context.Background(), "execute", "E_EXEC", false))

	require.NoError(t, h.QueueStrictTask(context.Background(), "E_FIN", 8))
	require.NoError(t, h.SetPortError(context.Background(), "finalize", "E_FIN", true))
	require.NoError(t, h.Poll(context.Background()))
	require.NoError(t, h.WaitCallCount(context.Background(), "finalize", 2, 3000))
	require.NoError(t, h.WaitRuntimeErrorCount(context.Background(), 2, 3000))
	require.NoError(t, h.SetPortError(context.Background(), "finalize", "E_FIN", false))

	require.NoError(t, h.SetRefreshConfig(context.Background(), "req-err", 2, 50, 1, 1, 0))
	require.NoError(t, h.SetPortError(context.Background(), "refresh_config", "", true))
	require.NoError(t, h.NotifyRuntimeConfig(context.Background(), "req-err"))
	require.NoError(t, h.WaitRuntimeErrorCount(context.Background(), 3, 3000))
	require.NoError(t, h.SetPortError(context.Background(), "refresh_config", "", false))

	require.NoError(t, h.SetPortError(context.Background(), "heartbeat", "", true))
	require.NoError(t, h.EmitEvent(context.Background(), "heartbeat_tick", 0, "", 0))
	require.NoError(t, h.WaitRuntimeErrorCount(context.Background(), 4, 3000))
	require.NoError(t, h.SetPortError(context.Background(), "heartbeat", "", false))

	require.NoError(t, h.SetPortError(context.Background(), "ack_config", "", true))
	require.NoError(t, h.NotifyRuntimeConfig(context.Background(), "req-err"))
	require.NoError(t, h.WaitRuntimeErrorCount(context.Background(), 5, 3000))
	require.NoError(t, h.AssertRuntimeErrorContains(context.Background(), "ack_config"))
	require.NoError(t, h.AssertInvariants(context.Background()))
}

func TestInvariantSweepGo(t *testing.T) {
	h := newRuntimeHarness()
	t.Cleanup(func() { _ = h.Stop(context.Background()) })

	require.NoError(t, h.Start(context.Background(), 3, 50))
	require.NoError(t, h.SetExecuteBlocking(context.Background(), true))

	var x uint32 = 17
	for i := 0; i < 120; i++ {
		x = x*1664525 + 1013904223
		action := x % 8
		switch action {
		case 0:
			require.NoError(t, h.QueueStrictTask(context.Background(), fmt.Sprintf("PX_%d", i), int32((i%10)+1)))
		case 1:
			require.NoError(t, h.QueueNormalTask(context.Background(), fmt.Sprintf("PN_%d", i), 0))
		case 2:
			require.NoError(t, h.Poll(context.Background()))
		case 3:
			require.NoError(t, h.ReleaseExecutions(context.Background(), 1))
		case 4:
			require.NoError(t, h.EmitEvent(context.Background(), "finalize_result", 999, "", 0))
		case 5:
			req := fmt.Sprintf("req-sweep-%d", i)
			version := int32(i + 1)
			pct := int32(i % 101)
			require.NoError(t, h.SetRefreshConfig(context.Background(), req, version, pct, 1, 1, 1))
			require.NoError(t, h.NotifyRuntimeConfig(context.Background(), req))
		case 6:
			require.NoError(t, h.EmitEvent(context.Background(), "execute_result", 999, "", 0))
		case 7:
			require.NoError(t, h.EmitEvent(context.Background(), "poll_tick", 0, "", 0))
		}
		require.NoError(t, h.AssertInvariants(context.Background()))
	}

	require.NoError(t, h.SetExecuteBlocking(context.Background(), false))
	require.NoError(t, h.ReleaseExecutions(context.Background(), 200))
	for i := 0; i < 80; i++ {
		require.NoError(t, h.Poll(context.Background()))
	}
	require.NoError(t, h.WaitSnapshot(context.Background(), 0, 0, 0, 5000))
	require.NoError(t, h.AssertInvariants(context.Background()))
}
