package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/stretchr/testify/require"
)

type admissionPort struct {
	scriptedPort
	execute  func(context.Context, Task) error
	finalize func(context.Context, Task, error) error
	register func(context.Context, string, []string, int64) error
	control  *Task
}

func (p *admissionPort) RegisterWorker(ctx context.Context, id string, labels []string, version int64) error {
	if p.register != nil {
		return p.register(ctx, id, labels, version)
	}
	return p.scriptedPort.RegisterWorker(ctx, id, labels, version)
}

func (p *admissionPort) LookupTask(_ context.Context, id int32) (*Task, error) {
	return &Task{ID: id}, nil
}
func (p *admissionPort) ClaimByID(_ context.Context, id int32, _ ClaimRequest) (*Task, error) {
	return &Task{ID: id}, nil
}
func (p *admissionPort) ClaimControl(_ context.Context, _ ClaimRequest) (*Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	task := p.control
	p.control = nil
	return task, nil
}
func (p *admissionPort) ExecuteTask(ctx context.Context, task Task) error {
	return p.execute(ctx, task)
}
func (p *admissionPort) FinalizeTask(ctx context.Context, task Task, err error) error {
	if p.finalize != nil {
		return p.finalize(ctx, task, err)
	}
	return p.scriptedPort.FinalizeTask(ctx, task, err)
}

func startAdmissionRuntime(t *testing.T, port Port, concurrency, controlConcurrency int) *Runtime {
	t.Helper()
	rt := NewRuntime(NewEngine(EngineConfig{WorkerID: "w1", Concurrency: concurrency, ControlConcurrency: controlConcurrency}), port,
		RuntimeOptions{PollInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); rt.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return rt
}

func receiveTask(t *testing.T, ch <-chan int32) int32 {
	t.Helper()
	select {
	case id := <-ch:
		return id
	case <-time.After(time.Second):
		t.Fatal("task did not start without another polling tick")
		return 0
	}
}

func TestRuntimeManualAndPolledTasksShareCapacityThroughFinalization(t *testing.T) {
	started := make(chan int32, 2)
	executeRelease := make(chan struct{})
	finalizeStarted := make(chan int32, 1)
	finalizeRelease := make(chan struct{})
	p := &admissionPort{scriptedPort: scriptedPort{normalResults: []scriptedClaimResult{{task: &Task{ID: 1}}}}}
	p.execute = func(ctx context.Context, task Task) error {
		started <- task.ID
		if task.ID == 1 {
			select {
			case <-executeRelease:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	p.finalize = func(ctx context.Context, task Task, _ error) error {
		if task.ID == 1 {
			finalizeStarted <- task.ID
			select {
			case <-finalizeRelease:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	rt := startAdmissionRuntime(t, p, 1, 0)
	require.Equal(t, int32(1), receiveTask(t, started))
	manualDone := make(chan error, 1)
	go func() { manualDone <- rt.RunTask(context.Background(), 2) }()
	require.Eventually(t, func() bool {
		s, ok := rt.Snapshot(context.Background())
		return ok && s.PendingRequests == 1 && s.InFlight == 1
	}, time.Second, time.Millisecond)
	close(executeRelease)
	require.Equal(t, int32(1), receiveTask(t, finalizeStarted))
	select {
	case id := <-started:
		t.Fatalf("task %d exceeded capacity while finalizing", id)
	default:
	}
	close(finalizeRelease)
	require.Equal(t, int32(2), receiveTask(t, started))
	require.NoError(t, <-manualDone)
}

func TestRuntimeFillsCapacityAndRefillsWithoutWaitingForPoll(t *testing.T) {
	started := make(chan int32, 4)
	release := make(chan struct{}, 4)
	var active, peak atomic.Int32
	p := &admissionPort{}
	for id := int32(1); id <= 4; id++ {
		p.normalResults = append(p.normalResults, scriptedClaimResult{task: &Task{ID: id}})
	}
	p.execute = func(ctx context.Context, task Task) error {
		count := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); count > old && !peak.CompareAndSwap(old, count); old = peak.Load() {
		}
		started <- task.ID
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	startAdmissionRuntime(t, p, 2, 0)
	receiveTask(t, started)
	receiveTask(t, started)
	release <- struct{}{}
	receiveTask(t, started)
	release <- struct{}{}
	receiveTask(t, started)
	require.Equal(t, int32(2), peak.Load())
}

func TestRuntimeControlTasksProgressWhileBusinessCapacityIsFull(t *testing.T) {
	started := make(chan int32, 2)
	controlDone := make(chan int32, 1)
	p := &admissionPort{scriptedPort: scriptedPort{normalResults: []scriptedClaimResult{{task: &Task{ID: 1}}}},
		control: &Task{ID: 2, Spec: apigen.TaskSpec{Type: "cancelTaskOnWorker"}}}
	p.execute = func(ctx context.Context, task Task) error {
		started <- task.ID
		if task.ID == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	p.finalize = func(_ context.Context, task Task, _ error) error {
		if task.ID == 2 {
			controlDone <- task.ID
		}
		return nil
	}
	rt := startAdmissionRuntime(t, p, 1, 1)
	ids := []int32{receiveTask(t, started), receiveTask(t, started)}
	require.ElementsMatch(t, []int32{1, 2}, ids)
	require.Equal(t, int32(2), receiveTask(t, controlDone))
	s, ok := rt.Snapshot(context.Background())
	require.True(t, ok)
	require.Equal(t, 1, s.InFlight)
}

func TestRuntimeCancellationDrainsFinalizationWithLiveContext(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "caller cancellation"
		if shutdown {
			name = "worker shutdown"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan int32, 1)
			finalizing := make(chan error, 1)
			release := make(chan struct{})
			p := &admissionPort{}
			p.execute = func(ctx context.Context, task Task) error { started <- task.ID; <-ctx.Done(); return ctx.Err() }
			p.finalize = func(ctx context.Context, _ Task, execErr error) error {
				if ctx.Err() != nil {
					finalizing <- ctx.Err()
					return ctx.Err()
				}
				finalizing <- execErr
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			rt := NewRuntime(NewEngine(EngineConfig{Concurrency: 1}), p, RuntimeOptions{})
			t.Cleanup(rt.Close)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			manualDone := make(chan error, 1)
			go func() { manualDone <- rt.RunTask(ctx, 1) }()
			receiveTask(t, started)
			closed := make(chan struct{})
			if shutdown {
				go func() { rt.Close(); close(closed) }()
			} else {
				cancel()
			}
			select {
			case err := <-finalizing:
				require.ErrorIs(t, err, taskcore.ErrTaskInterrupted)
			case <-time.After(time.Second):
				t.Fatal("finalization was dropped")
			}
			if shutdown {
				select {
				case <-closed:
					t.Fatal("shutdown returned before finalization")
				default:
				}
			}
			close(release)
			select {
			case <-manualDone:
			case <-time.After(time.Second):
				t.Fatal("manual request did not finish")
			}
			if shutdown {
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("shutdown did not drain")
				}
			}
		})
	}
}

func TestRuntimeRecoversExecutorPanicAndReleasesCapacity(t *testing.T) {
	finalized := make(chan error, 2)
	p := &admissionPort{}
	p.execute = func(_ context.Context, task Task) error {
		if task.ID == 1 {
			panic("broken handler")
		}
		return nil
	}
	p.finalize = func(_ context.Context, _ Task, err error) error { finalized <- err; return nil }
	rt := NewRuntime(NewEngine(EngineConfig{Concurrency: 1}), p, RuntimeOptions{})
	t.Cleanup(rt.Close)
	require.NoError(t, rt.RunTask(context.Background(), 1))
	require.ErrorContains(t, <-finalized, "broken handler")
	require.NoError(t, rt.RunTask(context.Background(), 2))
	require.NoError(t, <-finalized)
}

func TestRuntimeShutdownWaitsForStartupRegistrationBeforeMarkingOffline(t *testing.T) {
	registering := make(chan struct{})
	release := make(chan struct{})
	p := &admissionPort{}
	p.register = func(ctx context.Context, id string, labels []string, version int64) error {
		close(registering)
		<-release
		return p.scriptedPort.RegisterWorker(ctx, id, labels, version)
	}
	rt := NewRuntime(NewEngine(EngineConfig{Concurrency: 1}), p, RuntimeOptions{})
	done := make(chan struct{})
	go func() { defer close(done); rt.Start(context.Background()) }()
	select {
	case <-registering:
	case <-time.After(time.Second):
		t.Fatal("registration did not begin")
	}
	closed := make(chan struct{})
	go func() { rt.Close(); close(closed) }()
	require.Eventually(t, func() bool { return rt.ctx.Err() != nil }, time.Second, time.Millisecond)
	p.mu.Lock()
	require.Empty(t, p.offlineCalls)
	p.mu.Unlock()
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
	<-done
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Equal(t, []string{"refresh_config", "register", "offline"}, p.callOrder)
}
