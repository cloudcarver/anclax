package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/stretchr/testify/require"
)

type scriptedPort struct {
	mu sync.Mutex

	registerCalls []string
	offlineCalls  []string
	heartbeats    []string
	refreshReqIDs []string

	strictResults []scriptedClaimResult
	normalResults []scriptedClaimResult

	executeCalls  []int32
	finalizeCalls []int32

	refreshConfig *RuntimeConfig
	callOrder     []string
	lastAck       struct {
		requestID string
		version   int64
	}
}

type scriptedClaimResult struct {
	task *Task
	err  error
}

func (p *scriptedPort) RegisterWorker(ctx context.Context, workerID string, labels []string, appliedConfigVersion int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registerCalls = append(p.registerCalls, workerID)
	p.callOrder = append(p.callOrder, "register")
	return nil
}

func (p *scriptedPort) MarkWorkerOffline(ctx context.Context, workerID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.offlineCalls = append(p.offlineCalls, workerID)
	p.callOrder = append(p.callOrder, "offline")
	return nil
}

func (p *scriptedPort) ClaimStrict(ctx context.Context, req ClaimRequest) (*Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callOrder = append(p.callOrder, "claim_strict")
	if len(p.strictResults) == 0 {
		return nil, ErrNoTask
	}
	out := p.strictResults[0]
	p.strictResults = p.strictResults[1:]
	return copyTask(out.task), out.err
}

func (p *scriptedPort) ClaimNormalByGroup(ctx context.Context, req ClaimNormalRequest) (*Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callOrder = append(p.callOrder, "claim_normal:"+req.Group)
	if len(p.normalResults) == 0 {
		return nil, ErrNoTask
	}
	out := p.normalResults[0]
	p.normalResults = p.normalResults[1:]
	return copyTask(out.task), out.err
}

func (p *scriptedPort) ClaimByID(ctx context.Context, taskID int32, req ClaimRequest) (*Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callOrder = append(p.callOrder, "claim_by_id")
	return nil, ErrNoTask
}

func (p *scriptedPort) ExecuteTask(ctx context.Context, task Task) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.executeCalls = append(p.executeCalls, task.ID)
	p.callOrder = append(p.callOrder, "execute")
	return nil
}

func (p *scriptedPort) FinalizeTask(ctx context.Context, task Task, execErr error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finalizeCalls = append(p.finalizeCalls, task.ID)
	p.callOrder = append(p.callOrder, "finalize")
	return nil
}

func (p *scriptedPort) Heartbeat(ctx context.Context, workerID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.heartbeats = append(p.heartbeats, workerID)
	p.callOrder = append(p.callOrder, "heartbeat")
	return nil
}

func (p *scriptedPort) RefreshRuntimeConfig(ctx context.Context, workerID string, requestID string) (*RuntimeConfig, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshReqIDs = append(p.refreshReqIDs, requestID)
	p.callOrder = append(p.callOrder, "refresh_config")
	if p.refreshConfig == nil {
		return nil, nil
	}
	cfg := *p.refreshConfig
	cfg.LabelWeights = cloneWeights(cfg.LabelWeights)
	return &cfg, nil
}

func (p *scriptedPort) AckRuntimeConfigApplied(ctx context.Context, workerID string, requestID string, appliedVersion int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callOrder = append(p.callOrder, "ack_config")
	p.lastAck.requestID = requestID
	p.lastAck.version = appliedVersion
	return nil
}

func cloneWeights(src map[string]int32) map[string]int32 {
	if src == nil {
		return nil
	}
	out := make(map[string]int32, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func TestRuntimeStepRunsClaimExecuteFinalizeChain(t *testing.T) {
	eng := NewEngine(EngineConfig{
		WorkerID:            "w1",
		Concurrency:         1,
		MaxStrictPercentage: 100,
		LabelWeights:        map[string]int32{DefaultWeightConfigKey: 1},
	})
	port := &scriptedPort{
		strictResults: []scriptedClaimResult{{task: &Task{ID: 7, Priority: 10, Spec: apigen.TaskSpec{Type: "t"}}}},
	}
	rt := NewRuntime(eng, port, DefaultRuntimeOptions())
	t.Cleanup(rt.Close)

	rt.Step(context.Background(), Event{Type: EventPollTick})

	require.Eventually(t, func() bool {
		port.mu.Lock()
		defer port.mu.Unlock()
		return len(port.executeCalls) == 1 && len(port.finalizeCalls) == 1
	}, time.Second, 10*time.Millisecond)

	port.mu.Lock()
	require.Equal(t, []string{"claim_strict", "execute", "finalize"}, port.callOrder)
	require.Equal(t, []int32{7}, port.executeCalls)
	require.Equal(t, []int32{7}, port.finalizeCalls)
	port.mu.Unlock()
	require.Equal(t, 0, eng.Snapshot().ActiveCycles)
}

func TestRuntimeNotifyRuntimeConfig(t *testing.T) {
	eng := NewEngine(EngineConfig{WorkerID: "w1", Concurrency: 1})
	port := &scriptedPort{
		refreshConfig: &RuntimeConfig{
			Version:             9,
			MaxStrictPercentage: int32Ptr(25),
			LabelWeights: map[string]int32{
				DefaultWeightConfigKey: 1,
				"ops":                  2,
			},
		},
	}
	rt := NewRuntime(eng, port, DefaultRuntimeOptions())
	t.Cleanup(rt.Close)

	rt.NotifyRuntimeConfig(context.Background(), "req-9")

	require.Eventually(t, func() bool {
		port.mu.Lock()
		defer port.mu.Unlock()
		if len(port.refreshReqIDs) != 1 {
			return false
		}
		if port.refreshReqIDs[0] != "req-9" {
			return false
		}
		if port.lastAck.requestID != "req-9" || port.lastAck.version != int64(9) {
			return false
		}
		s := eng.Snapshot()
		return s.RuntimeConfigVersion == int64(9) && s.MaxStrictPercentage == int32(25)
	}, time.Second, 10*time.Millisecond)
}

func TestRuntimeStartRegistersAndMarksOfflineOnStop(t *testing.T) {
	eng := NewEngine(EngineConfig{WorkerID: "w-start", Concurrency: 1})
	port := &scriptedPort{}
	rt := NewRuntime(eng, port, RuntimeOptions{
		PollInterval:          0,
		HeartbeatInterval:     0,
		RuntimeConfigInterval: 0,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.Start(ctx)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runtime.Start did not stop")
	}

	require.Equal(t, []string{"w-start"}, port.registerCalls)
	require.Equal(t, []string{"w-start"}, port.offlineCalls)
}
