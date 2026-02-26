package workerv2

import (
	"testing"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/stretchr/testify/require"
)

func TestEngineStrictFallbackToNormalLifecycle(t *testing.T) {
	e := NewEngine(EngineConfig{
		WorkerID:            "w1",
		Concurrency:         1,
		MaxStrictPercentage: 100,
		LabelWeights: map[string]int32{
			DefaultWeightConfigKey: 1,
			"w1":                   2,
		},
	})

	cmds := e.Apply(Event{Type: EventPollTick})
	require.Len(t, cmds, 1)
	require.Equal(t, CmdClaimStrict, cmds[0].Type)
	cycleID := cmds[0].CycleID

	cmds = e.Apply(Event{Type: EventClaimStrictResult, CycleID: cycleID, Task: nil})
	require.Len(t, cmds, 1)
	require.Equal(t, CmdClaimNormal, cmds[0].Type)
	require.Equal(t, cycleID, cmds[0].CycleID)

	task := &Task{ID: 11, Priority: 0, Spec: apigen.TaskSpec{Type: "demo"}}
	cmds = e.Apply(Event{Type: EventClaimNormalResult, CycleID: cycleID, Task: task})
	require.Len(t, cmds, 1)
	require.Equal(t, CmdExecuteTask, cmds[0].Type)
	require.Equal(t, int32(11), cmds[0].Task.ID)

	cmds = e.Apply(Event{Type: EventExecuteResult, CycleID: cycleID, ExecErr: nil})
	require.Len(t, cmds, 1)
	require.Equal(t, CmdFinalize, cmds[0].Type)

	cmds = e.Apply(Event{Type: EventFinalizeResult, CycleID: cycleID})
	require.Empty(t, cmds)

	s := e.Snapshot()
	require.Equal(t, 0, s.InFlight)
	require.Equal(t, 0, s.StrictInFlight)
	require.Equal(t, 0, s.ActiveCycles)
}

func TestEngineNormalGroupProbeOrderDeterministic(t *testing.T) {
	e := NewEngine(EngineConfig{
		WorkerID:            "w1",
		Concurrency:         1,
		MaxStrictPercentage: 0,
		LabelWeights: map[string]int32{
			DefaultWeightConfigKey: 1,
			"w1":                   2,
			"w2":                   1,
		},
	})

	cmds := e.Apply(Event{Type: EventPollTick})
	require.Len(t, cmds, 1)
	require.Equal(t, CmdClaimNormal, cmds[0].Type)
	require.Equal(t, DefaultWeightGroup, cmds[0].Group)
	cycleID := cmds[0].CycleID

	cmds = e.Apply(Event{Type: EventClaimNormalResult, CycleID: cycleID, Task: nil})
	require.Len(t, cmds, 1)
	require.Equal(t, "w1", cmds[0].Group)

	cmds = e.Apply(Event{Type: EventClaimNormalResult, CycleID: cycleID, Task: nil})
	require.Len(t, cmds, 1)
	require.Equal(t, "w2", cmds[0].Group)

	cmds = e.Apply(Event{Type: EventClaimNormalResult, CycleID: cycleID, Task: nil})
	require.Empty(t, cmds)

	s := e.Snapshot()
	require.Equal(t, 0, s.ActiveCycles)
	require.Equal(t, 0, s.InFlight)

	// Next poll rotates cursor, so probe order starts from w1 instead of default.
	cmds = e.Apply(Event{Type: EventPollTick})
	require.Len(t, cmds, 1)
	require.Equal(t, CmdClaimNormal, cmds[0].Type)
	require.Equal(t, "w1", cmds[0].Group)
}

func TestEngineAppliesNewRuntimeConfigVersion(t *testing.T) {
	e := NewEngine(EngineConfig{
		WorkerID:            "w1",
		Concurrency:         10,
		MaxStrictPercentage: 100,
		LabelWeights: map[string]int32{
			DefaultWeightConfigKey: 1,
		},
	})

	e.Apply(Event{
		Type: EventRuntimeConfigLoaded,
		Config: &RuntimeConfig{
			Version:             2,
			MaxStrictPercentage: int32Ptr(25),
			LabelWeights: map[string]int32{
				DefaultWeightConfigKey: 1,
				"ops":                  3,
			},
		},
	})

	s := e.Snapshot()
	require.Equal(t, int64(2), s.RuntimeConfigVersion)
	require.Equal(t, int32(25), s.MaxStrictPercentage)
	require.Equal(t, 3, s.StrictCap)
	require.Equal(t, []string{"ops"}, s.WeightedLabels)
}

func TestEngineIgnoreStaleRuntimeConfigVersion(t *testing.T) {
	e := NewEngine(EngineConfig{WorkerID: "w1", Concurrency: 1})
	e.Apply(Event{Type: EventRuntimeConfigLoaded, Config: &RuntimeConfig{Version: 3, MaxStrictPercentage: int32Ptr(0)}})
	e.Apply(Event{Type: EventRuntimeConfigLoaded, Config: &RuntimeConfig{Version: 2, MaxStrictPercentage: int32Ptr(100)}})
	require.Equal(t, int64(3), e.Snapshot().RuntimeConfigVersion)
}
