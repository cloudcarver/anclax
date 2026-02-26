package workerv2

import (
	"context"
	"errors"
	"sync"
	"time"
)

type RuntimeOptions struct {
	PollInterval          time.Duration
	HeartbeatInterval     time.Duration
	RuntimeConfigInterval time.Duration
	OnError               func(error)
}

func DefaultRuntimeOptions() RuntimeOptions {
	return RuntimeOptions{
		PollInterval:          time.Second,
		HeartbeatInterval:     3 * time.Second,
		RuntimeConfigInterval: 0,
	}
}

type runtimeEnvelope struct {
	ctx   context.Context
	event Event
	done  chan struct{}
}

type Runtime struct {
	engine *Engine
	port   Port
	opts   RuntimeOptions

	inbox chan runtimeEnvelope

	stopOnce sync.Once
	stopCh   chan struct{}
	loopDone chan struct{}
}

func NewRuntime(engine *Engine, port Port, opts RuntimeOptions) *Runtime {
	if opts.PollInterval < 0 {
		opts.PollInterval = 0
	}
	if opts.HeartbeatInterval < 0 {
		opts.HeartbeatInterval = 0
	}
	if opts.RuntimeConfigInterval < 0 {
		opts.RuntimeConfigInterval = 0
	}

	r := &Runtime{
		engine:   engine,
		port:     port,
		opts:     opts,
		inbox:    make(chan runtimeEnvelope, 2048),
		stopCh:   make(chan struct{}),
		loopDone: make(chan struct{}),
	}
	go r.eventLoop()
	return r
}

func (r *Runtime) Close() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		<-r.loopDone
	})
}

func (r *Runtime) NotifyRuntimeConfig(ctx context.Context, requestID string) {
	r.enqueue(ctx, Event{Type: EventRuntimeConfigNotify, RequestID: requestID}, false)
}

// Step submits one external event to the runtime loop and waits until this
// event has been reduced (including all synchronous command->event chains).
func (r *Runtime) Step(ctx context.Context, event Event) {
	r.enqueue(ctx, event, true)
}

func (r *Runtime) enqueue(ctx context.Context, event Event, wait bool) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	env := runtimeEnvelope{
		ctx:   ctx,
		event: event,
	}
	if wait {
		env.done = make(chan struct{})
	}

	select {
	case <-r.loopDone:
		return false
	case r.inbox <- env:
	}

	if env.done != nil {
		select {
		case <-env.done:
			return true
		case <-r.loopDone:
			return false
		}
	}
	return true
}

func (r *Runtime) eventLoop() {
	defer close(r.loopDone)

	for {
		select {
		case <-r.stopCh:
			for {
				select {
				case env := <-r.inbox:
					if env.done != nil {
						close(env.done)
					}
				default:
					return
				}
			}
		case env := <-r.inbox:
			r.processEvent(env.ctx, env.event)
			if env.done != nil {
				close(env.done)
			}
		}
	}
}

// processEvent drains all resulting command->event chains in deterministic FIFO order.
func (r *Runtime) processEvent(ctx context.Context, event Event) {
	queue := []Event{event}
	for len(queue) > 0 {
		ev := queue[0]
		queue = queue[1:]

		commands := r.engine.Apply(ev)
		for _, cmd := range commands {
			queue = append(queue, r.execCommand(ctx, cmd)...)
		}
	}
}

func (r *Runtime) Start(ctx context.Context) {
	defer r.Close()

	if err := r.port.RegisterWorker(ctx, r.engine.WorkerID(), r.engine.Labels(), r.engine.CurrentRuntimeConfigVersion()); err != nil {
		r.handleError(err)
		return
	}

	// Startup catch-up for runtime config.
	r.Step(ctx, Event{Type: EventRuntimeConfigTick})

	var (
		pollTicker   *time.Ticker
		heartTicker  *time.Ticker
		configTicker *time.Ticker

		pollCh   <-chan time.Time
		heartCh  <-chan time.Time
		configCh <-chan time.Time
	)

	if r.opts.PollInterval > 0 {
		pollTicker = time.NewTicker(r.opts.PollInterval)
		pollCh = pollTicker.C
		defer pollTicker.Stop()
	}
	if r.opts.HeartbeatInterval > 0 {
		heartTicker = time.NewTicker(r.opts.HeartbeatInterval)
		heartCh = heartTicker.C
		defer heartTicker.Stop()
	}
	if r.opts.RuntimeConfigInterval > 0 {
		configTicker = time.NewTicker(r.opts.RuntimeConfigInterval)
		configCh = configTicker.C
		defer configTicker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			r.Step(context.Background(), Event{Type: EventStop})
			return
		case <-pollCh:
			r.enqueue(ctx, Event{Type: EventPollTick}, false)
		case <-heartCh:
			r.enqueue(ctx, Event{Type: EventHeartbeatTick}, false)
		case <-configCh:
			r.enqueue(ctx, Event{Type: EventRuntimeConfigTick}, false)
		}
	}
}

func (r *Runtime) execCommand(ctx context.Context, cmd Command) []Event {
	switch cmd.Type {
	case CmdClaimStrict:
		workerID := r.engine.WorkerID()
		labels := r.engine.Labels()
		hasLabels := len(labels) > 0
		cycleID := cmd.CycleID

		go func() {
			task, err := r.port.ClaimStrict(ctx, ClaimRequest{
				WorkerID:  workerID,
				Labels:    labels,
				HasLabels: hasLabels,
			})
			if errors.Is(err, ErrNoTask) {
				err = nil
				task = nil
			} else if err != nil {
				r.handleError(err)
			}
			r.enqueue(ctx, Event{Type: EventClaimStrictResult, CycleID: cycleID, Task: copyTask(task), Err: err}, false)
		}()
		return nil
	case CmdClaimNormal:
		workerID := r.engine.WorkerID()
		labels := r.engine.Labels()
		hasLabels := len(labels) > 0
		cycleID := cmd.CycleID
		group := cmd.Group
		weighted := append([]string(nil), cmd.WeightedLabels...)

		go func() {
			task, err := r.port.ClaimNormalByGroup(ctx, ClaimNormalRequest{
				ClaimRequest: ClaimRequest{
					WorkerID:  workerID,
					Labels:    labels,
					HasLabels: hasLabels,
				},
				Group:          group,
				WeightedLabels: weighted,
			})
			if errors.Is(err, ErrNoTask) {
				err = nil
				task = nil
			} else if err != nil {
				r.handleError(err)
			}
			r.enqueue(ctx, Event{Type: EventClaimNormalResult, CycleID: cycleID, Task: copyTask(task), Err: err}, false)
		}()
		return nil
	case CmdExecuteTask:
		if cmd.Task == nil {
			return []Event{{Type: EventExecuteResult, CycleID: cmd.CycleID, ExecErr: nil}}
		}
		task := *cmd.Task
		cycleID := cmd.CycleID
		go func() {
			err := r.port.ExecuteTask(ctx, task)
			r.enqueue(ctx, Event{Type: EventExecuteResult, CycleID: cycleID, ExecErr: err}, false)
		}()
		return nil
	case CmdFinalize:
		if cmd.Task == nil {
			return []Event{{Type: EventFinalizeResult, CycleID: cmd.CycleID, Err: nil}}
		}
		task := *cmd.Task
		cycleID := cmd.CycleID
		execErr := cmd.ExecErr
		go func() {
			err := r.port.FinalizeTask(ctx, task, execErr)
			if err != nil {
				r.handleError(err)
			}
			r.enqueue(ctx, Event{Type: EventFinalizeResult, CycleID: cycleID, Err: err}, false)
		}()
		return nil
	case CmdHeartbeat:
		workerID := r.engine.WorkerID()
		go func() {
			if err := r.port.Heartbeat(ctx, workerID); err != nil {
				r.handleError(err)
			}
		}()
		return nil
	case CmdRefreshRuntimeConfig:
		workerID := r.engine.WorkerID()
		requestID := cmd.RequestID
		go func() {
			cfg, err := r.port.RefreshRuntimeConfig(ctx, workerID, requestID)
			if err != nil {
				r.handleError(err)
			}
			r.enqueue(ctx, Event{Type: EventRuntimeConfigLoaded, RequestID: requestID, Config: cfg, Err: err}, false)
		}()
		return nil
	case CmdAckRuntimeConfig:
		workerID := r.engine.WorkerID()
		requestID := cmd.RequestID
		appliedVersion := cmd.AppliedVersion
		go func() {
			if err := r.port.AckRuntimeConfigApplied(ctx, workerID, requestID, appliedVersion); err != nil {
				r.handleError(err)
			}
		}()
		return nil
	case CmdMarkOffline:
		if err := r.port.MarkWorkerOffline(ctx, r.engine.WorkerID()); err != nil {
			r.handleError(err)
		}
		return nil
	default:
		return nil
	}
}

func (r *Runtime) handleError(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if r.opts.OnError != nil {
		r.opts.OnError(err)
	}
}
