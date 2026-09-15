package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBatchReservesSlotsStrictCapacityAndSerializesClaims(t *testing.T) {
	e := NewEngine(EngineConfig{Concurrency: 100, ClaimBatchSize: 32, MaxStrictPercentage: 10})
	cmd := e.Apply(Event{Type: EventPollTick})[0]
	require.Equal(t, CmdClaimBatch, cmd.Type)
	require.Equal(t, 32, cmd.BatchSize)
	require.Equal(t, 10, cmd.StrictSlots)
	require.Equal(t, 32, e.Snapshot().Claiming)
	require.Empty(t, e.Apply(Event{Type: EventPollTick}))
	manual := e.Apply(Event{Type: EventRunTask, TaskID: 90, RequestID: "manual"})
	require.Len(t, manual, 1)
	tasks := []*Task{{ID: 1, Priority: 1}, {ID: 2}}
	commands := e.Apply(Event{Type: EventClaimBatchResult, CycleID: cmd.CycleID, Tasks: tasks})
	require.Len(t, commands, 2, "a partial batch does not trigger another query")
	s := e.Snapshot()
	require.Equal(t, 3, s.InFlight)
	require.Equal(t, 1, s.StrictInFlight)
	require.Equal(t, 2, s.Executing)
	require.Equal(t, 1, s.Claiming)
	next := e.Apply(Event{Type: EventPollTick})[0]
	require.Equal(t, 9, next.StrictSlots)
}

func TestFullBatchRefillsAndErrorsReleaseReservations(t *testing.T) {
	e := NewEngine(EngineConfig{Concurrency: 5, ClaimBatchSize: 3})
	first := e.Apply(Event{Type: EventPollTick})[0]
	cmds := e.Apply(Event{Type: EventClaimBatchResult, CycleID: first.CycleID, Tasks: []*Task{{ID: 1}, {ID: 2}, {ID: 3}}})
	require.Len(t, cmds, 4)
	require.Equal(t, CmdClaimBatch, cmds[3].Type)
	require.Equal(t, 2, cmds[3].BatchSize)
	require.Equal(t, 5, e.Snapshot().InFlight)
	require.Empty(t, e.Apply(Event{Type: EventClaimBatchResult, CycleID: cmds[3].CycleID, Err: errors.New("database error")}))
	require.Equal(t, 3, e.Snapshot().InFlight)
	require.Zero(t, e.Snapshot().Claiming)
}

func TestBatchReturnedAfterStopStillFinalizesAndConfigCannotOverbook(t *testing.T) {
	e := NewEngine(EngineConfig{Concurrency: 4, ClaimBatchSize: 4, MaxStrictPercentage: 100})
	cmd := e.Apply(Event{Type: EventPollTick})[0]
	zero := int32(0)
	e.Apply(Event{Type: EventRuntimeConfigLoaded, Config: &RuntimeConfig{Version: 1, MaxStrictPercentage: &zero}})
	e.Apply(Event{Type: EventStop})
	exec := e.Apply(Event{Type: EventClaimBatchResult, CycleID: cmd.CycleID, Tasks: []*Task{{ID: 1, Priority: 1}}})
	require.Len(t, exec, 1)
	require.Equal(t, 1, e.Snapshot().InFlight)
	finalize := e.Apply(Event{Type: EventExecuteResult, CycleID: exec[0].CycleID, ExecErr: context.Canceled})
	require.Equal(t, CmdFinalize, finalize[0].Type)
	e.Apply(Event{Type: EventFinalizeResult, CycleID: exec[0].CycleID})
	require.Zero(t, e.Snapshot().InFlight)
	require.Zero(t, e.Snapshot().StrictInFlight)
}

type gatedBatchPort struct {
	admissionPort
	claimed chan ClaimBatchRequest
	release chan struct{}
}

func (p *gatedBatchPort) ClaimBatch(ctx context.Context, req ClaimBatchRequest) ([]*Task, error) {
	select {
	case p.claimed <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-p.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRuntimeDoesNotFanOutBatchClaimsOrSpinOnEmpty(t *testing.T) {
	p := &gatedBatchPort{claimed: make(chan ClaimBatchRequest, 100), release: make(chan struct{})}
	r := NewRuntime(NewEngine(EngineConfig{Concurrency: 100, ClaimBatchSize: 32}), p, RuntimeOptions{PollInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case req := <-p.claimed:
		require.Equal(t, 32, req.BatchSize)
	case <-time.After(time.Second):
		t.Fatal("batch did not start")
	}
	for i := 0; i < 20; i++ {
		r.Step(ctx, Event{Type: EventPollTick})
	}
	require.Empty(t, p.claimed)
	close(p.release)
	require.Eventually(t, func() bool { s, ok := r.Snapshot(ctx); return ok && s.InFlight == 0 }, time.Second, time.Millisecond)
	require.Empty(t, p.claimed)
}

func TestBatchRotationPreservesConfiguredGroupWeights(t *testing.T) {
	e := NewEngine(EngineConfig{Concurrency: 8, ClaimBatchSize: 4,
		LabelWeights: map[string]int32{"a": 2, "b": 1}})
	counts := map[string]int{}
	for i := 0; i < 4; i++ {
		cmd := e.Apply(Event{Type: EventPollTick})[0]
		counts[cmd.Groups[0]]++
		require.ElementsMatch(t, []string{"a", "b", DefaultWeightGroup}, cmd.Groups)
		require.Equal(t, []string{"a", "b"}, cmd.WeightedLabels)
		e.Apply(Event{Type: EventClaimBatchResult, CycleID: cmd.CycleID})
	}
	require.Equal(t, map[string]int{"a": 2, "b": 1, DefaultWeightGroup: 1}, counts)
}

func TestRuntimeReportsUnsupportedBatchPortAndReleasesSlots(t *testing.T) {
	errs := make(chan error, 1)
	r := NewRuntime(NewEngine(EngineConfig{Concurrency: 100, ClaimBatchSize: 32}), &admissionPort{},
		RuntimeOptions{PollInterval: time.Hour, OnError: func(err error) { errs <- err }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case err := <-errs:
		require.ErrorContains(t, err, "requires a BatchPort")
	case <-time.After(time.Second):
		t.Fatal("unsupported batch port was not reported")
	}
	require.Eventually(t, func() bool { s, ok := r.Snapshot(ctx); return ok && s.InFlight == 0 }, time.Second, time.Millisecond)
}
